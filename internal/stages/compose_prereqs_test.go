package stages

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteEnvFilesCreatesOnlyWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("KEEP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	written, err := writeEnvFiles(dir, []string{".env", "config/extra.env"}, map[string]string{"TOKEN": "s3cret"})
	if err != nil {
		t.Fatalf("writeEnvFiles = %v", err)
	}
	if !slices.Equal(written, []string{"config/extra.env"}) {
		t.Fatalf("written = %v, want [config/extra.env] — an existing file is the user's", written)
	}

	kept, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(kept) != "KEEP=1\n" {
		t.Fatalf(".env = %q, want it untouched", kept)
	}
	created, err := os.ReadFile(filepath.Join(dir, "config/extra.env"))
	if err != nil {
		t.Fatalf("read created env file: %v", err)
	}
	if string(created) != "TOKEN=\"s3cret\"\n" {
		t.Fatalf("created = %q, want the deployment's vars", created)
	}
}

func TestWriteEnvFilesRefusesToEscapeTheWorkdir(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "..", "escaped.env")

	if _, err := writeEnvFiles(dir, []string{outside}, map[string]string{"TOKEN": "s3cret"}); err == nil {
		t.Fatal("writeEnvFiles must refuse a path outside the workdir")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("writeEnvFiles wrote outside the workdir")
	}
}

func TestWriteEnvFilesWithNoVarsStillSatisfiesTheDeclaration(t *testing.T) {
	dir := t.TempDir()
	written, err := writeEnvFiles(dir, []string{".env"}, map[string]string{})
	if err != nil {
		t.Fatalf("writeEnvFiles = %v", err)
	}
	if !slices.Equal(written, []string{".env"}) {
		t.Fatalf("written = %v, want [.env] — compose still needs the file to exist", written)
	}
}
