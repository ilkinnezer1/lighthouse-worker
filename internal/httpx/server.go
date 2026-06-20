package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/docker/docker/client"

	dbgen "github.com/ilkinnezer1/lighthouse-worker/internal/db/gen"
	"github.com/ilkinnezer1/lighthouse-worker/internal/logger"
)

type Server struct {
	srv *http.Server
	log *slog.Logger
}

func New(addr string, log *slog.Logger, docker *client.Client, q *dbgen.Queries) *Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /deployments/{id}/logs", runtimeLogs(docker, q, logger.Ctx(log, "RuntimeLogs")))
	mux.HandleFunc("POST /deployments/{id}/services/{name}/{action}", serviceAction(docker, q, logger.Ctx(log, "ServiceAction")))
	mux.HandleFunc("POST /deployments/{id}/services/{name}/run", maintenanceRun(docker, q, logger.Ctx(log, "MaintenanceRun")))
	mux.HandleFunc("GET /deployments/{id}/services/{name}/stats", serviceStats(docker, q, logger.Ctx(log, "ServiceStats")))
	mux.HandleFunc("GET /deployments/{id}/services/{name}/health", serviceHealth(docker, q, logger.Ctx(log, "ServiceHealth")))
	mux.HandleFunc("GET /deployments/{id}/services/{name}/exec", containerExec(docker, q, logger.Ctx(log, "ContainerExec")))

	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           recoverer(log, mux),
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: logger.Ctx(log, "Http"),
	}
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("listening", slog.String("addr", s.srv.Addr))
		err := s.srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	rlog := logger.Ctx(log, "Http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				rlog.Error("panic in handler",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
