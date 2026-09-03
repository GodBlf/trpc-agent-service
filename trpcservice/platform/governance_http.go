package platform

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (h *AdminHandler) handleGovernance(w http.ResponseWriter, r *http.Request) {
	tenant, ok := trustedTenant(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "identity_required", "authenticated identity is required")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admin/governance/"), "/")
	switch {
	case path == "policy":
		h.handleGovernancePolicy(w, r, tenant)
	case path == "audit":
		h.handleGovernanceAudit(w, r, tenant)
	case path == "metrics":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
			return
		}
		writeJSON(w, http.StatusOK, h.governance.Metrics(tenant.TenantID))
	case path == "traces":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
			return
		}
		trace, found := h.governance.Trace(tenant.TenantID, strings.TrimSpace(r.URL.Query().Get("trace_id")), strings.TrimSpace(r.URL.Query().Get("request_id")))
		if !found {
			writeError(w, http.StatusNotFound, "trace_not_found", "trace was not found")
			return
		}
		writeJSON(w, http.StatusOK, trace)
	case path == "confirmations":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": h.governance.Confirmations(tenant.TenantID)})
	case strings.HasPrefix(path, "confirmations/") && strings.HasSuffix(path, "/decision"):
		h.handleConfirmationDecision(w, r, tenant, strings.TrimSuffix(strings.TrimPrefix(path, "confirmations/"), "/decision"))
	default:
		http.NotFound(w, r)
	}
}

func (h *AdminHandler) handleGovernancePolicy(w http.ResponseWriter, r *http.Request, tenant TenantContext) {
	switch r.Method {
	case http.MethodGet:
		appID := strings.TrimSpace(r.URL.Query().Get("app_id"))
		policy, found := h.governance.Policy(tenant.TenantID, appID)
		if !found {
			writeError(w, http.StatusNotFound, "policy_not_found", "governance policy was not found")
			return
		}
		writeJSON(w, http.StatusOK, publicPolicy(policy))
	case http.MethodPost, http.MethodPut:
		if !canMutate(tenant.Role) {
			writeError(w, http.StatusForbidden, "forbidden", "tenant administrator role is required")
			return
		}
		var policy TenantPolicy
		if err := decodeStrict(r, &policy); err != nil || policy.AgentAppID == "" {
			writeError(w, http.StatusBadRequest, "invalid_policy", "governance policy is invalid")
			return
		}
		if _, found := h.platform.app(tenant.TenantID, policy.AgentAppID); !found {
			writeError(w, http.StatusNotFound, "agent_app_not_found", "Agent App was not found")
			return
		}
		policy.TenantID = tenant.TenantID
		if existing, found := h.governance.Policy(tenant.TenantID, policy.AgentAppID); found {
			replacements := []string{}
			for _, value := range policy.RedactedPatterns {
				if value != "[REDACTED]" {
					replacements = append(replacements, value)
				}
			}
			if len(replacements) == 0 {
				policy.RedactedPatterns = existing.RedactedPatterns
			} else {
				policy.RedactedPatterns = replacements
			}
		}
		updated, err := h.governance.PutPolicy(r.Context(), policy)
		if err != nil {
			if err.Error() == "invalid_policy" {
				writeError(w, http.StatusBadRequest, "invalid_policy", "governance policy is invalid")
			} else {
				writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
			}
			return
		}
		writeJSON(w, http.StatusOK, publicPolicy(updated))
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET, POST, or PUT")
	}
}

func publicPolicy(policy TenantPolicy) TenantPolicy {
	for index := range policy.RedactedPatterns {
		policy.RedactedPatterns[index] = "[REDACTED]"
	}
	return policy
}

func (h *AdminHandler) handleGovernanceAudit(w http.ResponseWriter, r *http.Request, tenant TenantContext) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be GET")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	query := AuditQuery{
		TenantID: tenant.TenantID, Channel: r.URL.Query().Get("channel"), AgentName: r.URL.Query().Get("agent_name"),
		Decision: r.URL.Query().Get("decision"), ErrorType: r.URL.Query().Get("error_type"), UserID: r.URL.Query().Get("user_id"),
		SessionID: r.URL.Query().Get("session_id"), RequestID: r.URL.Query().Get("request_id"), TraceID: r.URL.Query().Get("trace_id"),
		From: from, To: to, Offset: offset, Limit: limit,
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": h.governance.AuditEvents(query)})
}

func (h *AdminHandler) handleConfirmationDecision(w http.ResponseWriter, r *http.Request, tenant TenantContext, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method must be POST")
		return
	}
	if !canOperate(tenant.Role) {
		writeError(w, http.StatusForbidden, "forbidden", "operator role is required")
		return
	}
	var request struct {
		Approve bool `json:"approve"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_confirmation_decision", "confirmation decision is invalid")
		return
	}
	confirmation, err := h.governance.DecideConfirmation(r.Context(), tenant.TenantID, id, tenant.UserID, request.Approve)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "confirmation_not_found", "confirmation was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, confirmation)
}
