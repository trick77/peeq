// Command vark is the all-in-one server: API + embedded SPA.
//
// This is a Task-1 skeleton: config, DB, auth, and the actual YouTube archiving
// pipeline arrive in later tasks. It exists to prove the repo, build, and
// container pipeline work end to end.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/trick77/vark/internal/version"
	"github.com/trick77/vark/web"
)

func main() {
	slog.Info("starting vark", "version", version.Version)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", web.Handler())

	addr := envDefault("VARK_ADDR", ":8080")
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func envDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
