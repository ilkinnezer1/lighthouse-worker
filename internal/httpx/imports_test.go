package httpx

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ilkinnezer1/lighthouse-worker/internal/database"
)

type fakeImportStore struct {
	got database.ImportUpload
	err error
}

func (f *fakeImportStore) StoreImport(_ context.Context, up database.ImportUpload) (int64, error) {
	f.got = up
	if f.err != nil {
		return 0, f.err
	}
	n, _ := io.Copy(io.Discard, up.Body)
	return n, nil
}

func serveImport(store ImportStore, body string, header http.Header) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /databases/{id}/snapshots/{snapshotId}/upload", importUpload(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	req := httptest.NewRequest(http.MethodPut, "/databases/db1/snapshots/snap1/upload", strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestImportUploadSuccess(t *testing.T) {
	store := &fakeImportStore{}
	rec := serveImport(store, "SELECT 1;", http.Header{"X-Import-Filename": {"wj%20dev%2B3.sql.gz"}})
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"sizeBytes":9}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if store.got.FileName != "wj dev+3.sql.gz" || store.got.DatabaseID != "db1" || store.got.SnapshotID != "snap1" || store.got.ContentLength != 9 {
		t.Fatalf("store got %+v", store.got)
	}
}

func TestImportUploadErrorStatus(t *testing.T) {
	cases := map[error]int{
		database.ErrImportNotFound:                               http.StatusNotFound,
		database.ErrImportConflict:                               http.StatusConflict,
		fmt.Errorf("%w (9 > 1)", database.ErrImportTooLarge):     http.StatusRequestEntityTooLarge,
		database.ErrImportLengthUnset:                            http.StatusLengthRequired,
		fmt.Errorf("%w: %q", database.ErrImportBadFile, "x.tar"): http.StatusBadRequest,
		fmt.Errorf("upload: boom"):                               http.StatusInternalServerError,
	}
	for err, want := range cases {
		rec := serveImport(&fakeImportStore{err: err}, "x", nil)
		if rec.Code != want {
			t.Errorf("%v: got %d want %d", err, rec.Code, want)
		}
		if !strings.Contains(rec.Body.String(), `"error":`) || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%v: body %s", err, rec.Body)
		}
	}
}
