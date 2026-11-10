package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/ilkinnezer1/lighthouse-worker/internal/database"
)

type ImportStore interface {
	StoreImport(ctx context.Context, up database.ImportUpload) (int64, error)
}

func importUpload(store ImportStore, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fileName, err := url.PathUnescape(r.Header.Get("X-Import-Filename"))
		if err != nil {
			fileName = r.Header.Get("X-Import-Filename")
		}
		size, err := store.StoreImport(r.Context(), database.ImportUpload{
			DatabaseID:    r.PathValue("id"),
			SnapshotID:    r.PathValue("snapshotId"),
			FileName:      fileName,
			ContentLength: r.ContentLength,
			Body:          r.Body,
		})
		if err != nil {
			status := importErrorStatus(err)
			log.Warn("import upload failed",
				slog.String("snapshotId", r.PathValue("snapshotId")),
				slog.Int("status", status),
				slog.Any("err", err),
			)
			writeJSONStatus(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSONStatus(w, http.StatusOK, map[string]int64{"sizeBytes": size})
	}
}

func importErrorStatus(err error) int {
	switch {
	case errors.Is(err, database.ErrImportNotFound):
		return http.StatusNotFound
	case errors.Is(err, database.ErrImportConflict):
		return http.StatusConflict
	case errors.Is(err, database.ErrImportTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, database.ErrImportLengthUnset):
		return http.StatusLengthRequired
	case errors.Is(err, database.ErrImportBadFile):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
