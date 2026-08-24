package web

import (
	"encoding/json"
	"net/http"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
	"github.com/liuzengh/trpc-agent-service/trpcservice/platform"
)

// NewHandler returns the baseline HTTP handler. The endpoints intentionally
// have no integration dependencies so they can be used for process and probe
// checks before the platform components are connected.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/version", versionHandler)
	return mux
}

// NewHandlerWithRunner adds the stage-0 platform loop and injects the trusted
// tenant context at the server boundary.
func NewHandlerWithRunner(runner platform.RunnerAdapter, tenant platform.TenantContext) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/version", versionHandler)
	run := platform.RunHandler{Runner: runner}
	mux.Handle("/v1/run", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		run.ServeHTTP(w, r.WithContext(platform.WithTenantContext(r.Context(), tenant)))
	}))
	return mux
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": trpcservice.Version})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// These responses are fixed and encoding cannot fail in practice. Keep the
	// handler independent of a logger while still returning valid JSON.
	_ = json.NewEncoder(w).Encode(value)
}
