package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
)

// snapshotExt labels the object in the bucket for a human reading it; nothing
// reads it back, the storage key is stored on the row.
func snapshotExt(t dbgen.DatabaseType) string {
	switch t {
	case dbgen.DatabaseTypeMYSQL:
		return "sql"
	case dbgen.DatabaseTypeMONGO:
		return "archive"
	case dbgen.DatabaseTypeREDIS:
		return "tar"
	default:
		return "dump"
	}
}

const stderrTailBytes = 4096

type engineCreds struct {
	user string
	pass string
}

// credsFor recovers the engine login from the encrypted connection string. That
// is the only place a provisioned password survives — MySQL's root password in
// particular is generated at provision time and deliberately never stored — so
// dumps run as the owning application user rather than as a superuser.
func (r *Router) credsFor(row dbgen.GetDatabaseRow) (engineCreds, error) {
	if row.ConnSecretEnc == nil || *row.ConnSecretEnc == "" {
		return engineCreds{}, errors.New("database has no stored connection secret")
	}
	plain, err := r.crypto.Decrypt(*row.ConnSecretEnc)
	if err != nil {
		return engineCreds{}, fmt.Errorf("decrypt connection secret: %w", err)
	}
	u, err := url.Parse(plain)
	if err != nil {
		return engineCreds{}, fmt.Errorf("parse connection string: %w", err)
	}
	pass, ok := u.User.Password()
	if !ok || pass == "" {
		return engineCreds{}, errors.New("connection string carries no password")
	}
	return engineCreds{user: u.User.Username(), pass: pass}, nil
}

// dumpStream starts the engine's dump tool and returns a reader over its stdout.
// finish must be called once the reader is drained: without it a tool that died
// mid-dump would upload its truncated output as though it were a backup.
func (r *Router) dumpStream(
	ctx context.Context,
	row dbgen.GetDatabaseRow,
	name string,
) (io.ReadCloser, func() error, error) {
	switch row.Type {
	case dbgen.DatabaseTypePOSTGRES:
		return r.execStream(ctx, name,
			[]string{"pg_dump", "--format=custom", "-U", postgresUser, row.Name}, nil)

	case dbgen.DatabaseTypeMYSQL:
		c, err := r.credsFor(row)
		if err != nil {
			return nil, nil, err
		}
		// MYSQL_PWD keeps the password out of the container's process list;
		// --single-transaction takes a consistent InnoDB dump without locking.
		return r.execStream(ctx, name,
			[]string{"mysqldump", "--single-transaction", "--routines", "--triggers", "-u", c.user, row.Name},
			[]string{"MYSQL_PWD=" + c.pass})

	case dbgen.DatabaseTypeMONGO:
		c, err := r.credsFor(row)
		if err != nil {
			return nil, nil, err
		}
		// mongodump has no password env var, so this one is on argv — visible
		// only to processes already inside the database's own container.
		return r.execStream(ctx, name,
			[]string{"mongodump", "--archive", "--db", row.Name,
				"--username", c.user, "--password", c.pass, "--authenticationDatabase", "admin"}, nil)

	case dbgen.DatabaseTypeREDIS:
		return r.redisBackupStream(ctx, name)

	default:
		return nil, nil, fmt.Errorf("snapshots are not supported for %s", row.Type)
	}
}

// restoreInto loads a dump into the database. With replace, the target schema is
// emptied first so objects the dump does not mention do not survive it.
func (r *Router) restoreInto(ctx context.Context, row dbgen.GetDatabaseRow, name string, src io.Reader, replace bool) error {
	switch row.Type {
	case dbgen.DatabaseTypePOSTGRES:
		if replace {
			if err := r.emptyPostgres(ctx, name, row.Name); err != nil {
				return err
			}
		}
		return r.pgRestore(ctx, name, row.Name, src)

	case dbgen.DatabaseTypeMYSQL:
		c, err := r.credsFor(row)
		if err != nil {
			return err
		}
		if replace {
			if err := r.emptyMysql(ctx, name, row.Name, c); err != nil {
				return err
			}
		}
		body, err := mysqlRestoreStream(src)
		if err != nil {
			return err
		}
		return r.execWithStdin(ctx, name,
			[]string{"mysql", "-u", c.user, row.Name}, []string{"MYSQL_PWD=" + c.pass}, body)

	case dbgen.DatabaseTypeMONGO:
		c, err := r.credsFor(row)
		if err != nil {
			return err
		}
		// --drop clears each collection in the archive before reloading it, so a
		// restore is a replacement rather than a merge.
		return r.execWithStdin(ctx, name,
			[]string{"mongorestore", "--archive", "--drop", "--nsInclude", row.Name + ".*",
				"--username", c.user, "--password", c.pass, "--authenticationDatabase", "admin"}, nil, src)

	case dbgen.DatabaseTypeREDIS:
		return r.redisRestore(ctx, name, src)

	default:
		return fmt.Errorf("restore is not supported for %s", row.Type)
	}
}

// execStream runs cmd in the container and streams its stdout through a pipe, so
// a dump larger than this process's memory never has to be held in it.
func (r *Router) execStream(
	ctx context.Context,
	containerName string,
	cmd []string,
	env []string,
) (io.ReadCloser, func() error, error) {
	exec, err := r.docker.ContainerExecCreate(ctx, containerName, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.docker.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("exec attach: %w", err)
	}

	pr, pw := io.Pipe()
	stderr := newTailBuffer(stderrTailBytes)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, copyErr := stdcopy.StdCopy(pw, stderr, attach.Reader)
		_ = pw.CloseWithError(copyErr)
	}()

	finish := func() error {
		<-drained
		attach.Close()
		exitCode, err := r.execExitCode(ctx, exec.ID)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			return fmt.Errorf("%s exit %d: %s", cmd[0], exitCode, stderr.String())
		}
		return nil
	}
	return pr, finish, nil
}

func (r *Router) execOutput(ctx context.Context, containerName string, cmd []string, env []string) ([]byte, error) {
	out, finish, err := r.execStream(ctx, containerName, cmd, env)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(out)
	if err := finish(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, fmt.Errorf("read %s output: %w", cmd[0], readErr)
	}
	return data, nil
}

// execWithStdin runs cmd in the container feeding src to its stdin, and fails on
// a non-zero exit carrying the tail of what the tool wrote to stderr.
func (r *Router) execWithStdin(
	ctx context.Context,
	containerName string,
	cmd []string,
	env []string,
	src io.Reader,
) error {
	exec, err := r.docker.ContainerExecCreate(ctx, containerName, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attach, err := r.docker.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	copyErrCh := make(chan error, 1)
	go func() {
		_, err := io.Copy(attach.Conn, src)
		_ = attach.CloseWrite()
		copyErrCh <- err
	}()

	stderr := newTailBuffer(stderrTailBytes)
	_, readErr := stdcopy.StdCopy(io.Discard, stderr, attach.Reader)

	exitCode, err := r.execExitCode(ctx, exec.ID)
	if err != nil {
		return err
	}
	// The exit code is checked before the copy result: a tool that stops at the
	// first bad statement closes its stdin, and the resulting broken pipe would
	// otherwise hide the engine's own error.
	if exitCode != 0 {
		return fmt.Errorf("%s exit %d: %s", cmd[0], exitCode, stderr.String())
	}
	if readErr != nil {
		return fmt.Errorf("read exec output: %w", readErr)
	}
	if err := <-copyErrCh; err != nil {
		return fmt.Errorf("stream dump to exec: %w", err)
	}
	return nil
}

// execExitCode waits out the short window where the output stream has closed
// but Docker still reports the exec as running with a placeholder exit code.
func (r *Router) execExitCode(ctx context.Context, execID string) (int, error) {
	for i := 0; i < 50; i++ {
		info, err := r.docker.ContainerExecInspect(ctx, execID)
		if err != nil {
			return 0, fmt.Errorf("exec inspect: %w", err)
		}
		if !info.Running {
			return info.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return 0, errors.New("exec still running after its output closed")
}
