package platform

import (
	"context"
	"net/http"
	"time"
)

type auditResponseWriter struct {
	http.ResponseWriter
	status int
	tenant TenantContext
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *auditResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}
func (w *auditResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func markAuditIdentity(w http.ResponseWriter, tenant TenantContext) {
	if audit, ok := w.(*auditResponseWriter); ok {
		audit.tenant = tenant
	}
}

func (h *AdminHandler) auditHTTPRequest(ctx context.Context, r *http.Request, response *auditResponseWriter, started time.Time) {
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	if r.Method == http.MethodGet && status < http.StatusBadRequest && r.URL.Path != "/api/v1/auth/me" {
		return
	}
	decision := "http.allowed"
	errorType := ""
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		decision, errorType = "authorization.denied", "forbidden"
	} else if status >= http.StatusBadRequest {
		decision, errorType = "http.failed", "request_failed"
	}
	requestID := r.Header.Get("X-Request-ID")
	if !validIdempotencyKey(requestID) {
		requestID = ""
	}
	_ = h.governance.Record(ctx, AuditEvent{TenantID: response.tenant.TenantID, UserID: response.tenant.UserID, Decision: decision, ErrorType: errorType, RequestID: requestID, TraceID: newTraceID(), Latency: time.Since(started), OccurredAt: time.Now().UTC()})
}
