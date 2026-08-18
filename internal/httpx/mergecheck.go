package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ilkinnezer1/lighthouse-worker/internal/crypto"
	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/stages"
)

// mergeCheck answers whether a branch set would combine cleanly, before anyone
// spends a build finding out. Nest resolves the branch names to shas and proxies
// here; the worker is the only side that can run git.
func mergeCheck(q *dbgen.Queries, cipher *crypto.Cipher, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req stages.MergeCheck
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if req.RepoURL == "" {
			http.Error(w, "repoUrl is required", http.StatusBadRequest)
			return
		}

		result, err := stages.CheckMerge(r.Context(), q, cipher, req)
		if err != nil {
			// A probe that could not run is not a conflict, and must not be
			// reported as one — the caller says "could not tell".
			log.Warn("merge check failed", slog.Any("err", err))
			http.Error(w, "merge check failed", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}
