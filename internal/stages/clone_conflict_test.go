package stages

import "testing"

// The web turns this message into "Merge conflict — <branch> into <into>" plus
// the file list, so QA sees the cause rather than a bare stage error. The two
// sides agree by wording alone; this test is the Go half of that contract.
// See lighthouse-web/src/features/deployments/failureReason.ts.
func TestMergeConflictMessageFormat(t *testing.T) {
	err := mergeConflictError("feature/PROJ-12-add-login", "main",
		[]string{"src/app.ts", "src/router.ts"})

	want := "merge feature/PROJ-12-add-login into main: conflicts in src/app.ts, src/router.ts"
	if err.Error() != want {
		t.Fatalf("the web parses this exact shape\n got: %s\nwant: %s", err.Error(), want)
	}
}

func TestMergeConflictMessageSingleFile(t *testing.T) {
	err := mergeConflictError("feat/a", "trunk", []string{"README.md"})

	want := "merge feat/a into trunk: conflicts in README.md"
	if err.Error() != want {
		t.Fatalf("got: %s\nwant: %s", err.Error(), want)
	}
}
