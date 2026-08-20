package stages

import (
	"os"
	"path/filepath"
	"testing"
)

const testDeployID = "00ab31f8-4c21-4895-93ff-d6b00f0aea30"

func TestOwnsImage(t *testing.T) {
	cases := []struct {
		img  string
		want bool
	}{
		{"deploy-" + testDeployID, true},
		{"deploy-" + testDeployID + "-php", true},
		{"deploy-" + testDeployID + "-nginx", true},
		{"mysql:8.0", false},
		{"node:20", false},
		{"postgres:16-alpine", false},
		{"deploy-7958a0aa-6916-4e42-9b58-f886da02c690", false},
		{"deploy-7958a0aa-6916-4e42-9b58-f886da02c690-php", false},
		{"", false},
	}
	for _, c := range cases {
		if got := OwnsImage(testDeployID, c.img); got != c.want {
			t.Errorf("OwnsImage(%q) = %v, want %v", c.img, got, c.want)
		}
	}
}

func TestOwnedImagesDropsUpstreamAndDedupes(t *testing.T) {
	got := OwnedImages(testDeployID, []string{
		"mysql:8.0",
		"deploy-" + testDeployID + "-php",
		"node:20",
		"deploy-" + testDeployID + "-php",
		"deploy-" + testDeployID,
		"<none>:<none>",
		"deploy-7958a0aa-6916-4e42-9b58-f886da02c690",
	})
	want := []string{
		"deploy-" + testDeployID,
		"deploy-" + testDeployID + "-php",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestOwnedImagesAlwaysIncludesOwnTag(t *testing.T) {
	got := OwnedImages(testDeployID, nil)
	if len(got) != 1 || got[0] != ImageTag(testDeployID) {
		t.Fatalf("got %v, want [%s]", got, ImageTag(testDeployID))
	}
}

// An empty or traversing id would resolve to the shared base dir and wipe every
// deployment on the host.
func TestReclaimWorkDirRefusesToTouchBase(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../..", "a/b", `a\b`} {
		base := t.TempDir()
		sentinel := filepath.Join(base, "other-deployment")
		if err := os.MkdirAll(sentinel, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReclaimWorkDir(base, id); err != nil {
			t.Fatalf("id %q: unexpected error %v", id, err)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatalf("id %q removed the shared base dir: %v", id, err)
		}
	}
	if _, _, err := ReclaimWorkDir("", testDeployID); err != nil {
		t.Fatalf("empty base: %v", err)
	}
}

func TestReclaimWorkDirReportsFreedBytes(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, testDeployID)
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "big"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	freed, residual, err := ReclaimWorkDir(base, testDeployID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if freed != 4096 {
		t.Errorf("freed = %d, want 4096", freed)
	}
	if residual != 0 {
		t.Errorf("residual = %d, want 0", residual)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Error("workdir still present")
	}
}

// The real failure on the user's host: build containers leave files the worker's
// uid cannot unlink. The reclaim must report that, not claim success.
func TestReclaimWorkDirReportsUnremovableTree(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	locked := filepath.Join(base, testDeployID, "node_modules")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "pinned"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	freed, residual, err := ReclaimWorkDir(base, testDeployID)
	if err == nil {
		t.Fatal("expected an error for a tree that could not be removed")
	}
	if residual != 2048 {
		t.Errorf("residual = %d, want 2048", residual)
	}
	if freed >= 2048 {
		t.Errorf("freed = %d, must not count bytes still on disk", freed)
	}
	r := Reclaimed{WorkDirBytes: freed, ResidualBytes: residual, Err: err}
	if r.Complete() {
		t.Error("Complete() must be false when the workdir survived")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KB", 1048576: "1.0 MB", 3865470566: "3.6 GB"}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
