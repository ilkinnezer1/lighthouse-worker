package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ilkinnezer1/lighthouse-worker/internal/db"
)

const (
	maxTreeEntries = 2000
	maxFileBytes   = 1 << 20 // 1 MiB
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

type fileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// filesTree recursively lists a deployment's cloned workdir for the file
// explorer. Read-only: no docker, no writes — pure filesystem under workDir.
func filesTree(workDir string, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		root, ok := deploymentWorkDir(w, workDir, r.PathValue("id"))
		if !ok {
			return
		}

		entries := make([]fileEntry, 0, 256)
		truncated := false
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root {
				return nil
			}
			if d.IsDir() && skipDirs[d.Name()] {
				return filepath.SkipDir
			}

			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			entry := fileEntry{
				Name: d.Name(),
				Path: filepath.ToSlash(rel),
				Type: "file",
			}
			if d.IsDir() {
				entry.Type = "dir"
			} else if info, ierr := d.Info(); ierr == nil {
				entry.Size = info.Size()
			}
			entries = append(entries, entry)

			if len(entries) >= maxTreeEntries {
				truncated = true
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			log.Error("walk workdir", slog.String("dir", root), slog.Any("err", err))
			http.Error(w, "read workdir", http.StatusInternalServerError)
			return
		}

		sort.Slice(entries, func(i, j int) bool {
			if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
				return entries[i].Type == "dir"
			}
			return entries[i].Path < entries[j].Path
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"entries":   entries,
			"truncated": truncated,
		})
	}
}

// filesContent returns the contents of a single file under a deployment's
// workdir, capped at 1 MiB. Binary files (NUL byte present) come back with an
// empty body and binary:true so the API/UI don't render garbage.
func filesContent(workDir string, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		root, ok := deploymentWorkDir(w, workDir, r.PathValue("id"))
		if !ok {
			return
		}

		target, rel, ok := safeResolve(w, root, r.URL.Query().Get("path"))
		if !ok {
			return
		}

		info, err := os.Stat(target)
		if err != nil || info.IsDir() {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}

		f, err := os.Open(target)
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		buf := make([]byte, maxFileBytes)
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Error("read file", slog.String("file", target), slog.Any("err", err))
			http.Error(w, "read file", http.StatusInternalServerError)
			return
		}
		data := buf[:n]
		truncated := info.Size() > maxFileBytes

		resp := map[string]any{
			"path":      rel,
			"truncated": truncated,
		}
		if bytes.IndexByte(data, 0) >= 0 {
			resp["binary"] = true
			resp["content"] = ""
		} else {
			resp["binary"] = false
			resp["content"] = string(data)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// deploymentWorkDir resolves the workdir the same way the deploy pipeline does
// (filepath.Join(workDir, id)); writes the error response itself on failure.
func deploymentWorkDir(w http.ResponseWriter, workDir, id string) (string, bool) {
	if _, err := db.ParseUUID(id); err != nil {
		http.Error(w, "invalid deployment id", http.StatusBadRequest)
		return "", false
	}
	root := filepath.Join(workDir, id)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		http.Error(w, "deployment workdir not found", http.StatusNotFound)
		return "", false
	}
	return root, true
}

// safeResolve cleans rel and guarantees the result stays within root, rejecting
// absolute paths and any "../" escape. Returns the absolute target and the
// slash-normalized relative path. Writes the error response itself.
func safeResolve(w http.ResponseWriter, root, rel string) (string, string, bool) {
	if rel == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return "", "", false
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		http.Error(w, "path must be relative", http.StatusBadRequest)
		return "", "", false
	}
	target := filepath.Join(root, clean)
	base := filepath.Clean(root)
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		http.Error(w, "path escapes workdir", http.StatusBadRequest)
		return "", "", false
	}
	return target, filepath.ToSlash(clean), true
}
