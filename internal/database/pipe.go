package database

import (
	"errors"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/stdcopy"
)

// pipeExecs streams the stdout of `src` (a docker exec attach) into the stdin
// of `dst` (another docker exec attach), draining `dst`'s stderr/stdout in the
// background. Used for pg_dump → pg_restore between two containers.
func pipeExecs(src, dst types.HijackedResponse) error {
	srcStdout, srcStdoutW := io.Pipe()
	srcStderrW := io.Discard

	dumpDone := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(srcStdoutW, srcStderrW, src.Reader)
		_ = srcStdoutW.Close()
		dumpDone <- err
	}()

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(dst.Conn, srcStdout)
		_ = dst.CloseWrite()
		copyDone <- err
	}()

	dstDone := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(io.Discard, io.Discard, dst.Reader)
		dstDone <- err
	}()

	var first error
	for _, ch := range []chan error{dumpDone, copyDone, dstDone} {
		if err := <-ch; err != nil && first == nil && !errors.Is(err, io.EOF) {
			first = err
		}
	}
	return first
}
