package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type TenantPolicy struct {
	TenantID              string    `json:"tenant_id"`
	AgentAppID            string    `json:"agent_app_id"`
	Revision              uint64    `json:"revision"`
	AllowedTools          []string  `json:"allowed_tools"`
	AllowedMCP            []string  `json:"allowed_mcp"`
	DangerousTools        []string  `json:"dangerous_tools"`
	DeniedInputPatterns   []string  `json:"denied_input_patterns"`
	DeniedOutputPatterns  []string  `json:"denied_output_patterns"`
	RedactedPatterns      []string  `json:"redacted_patterns"`
	AllowedIMUsers        []string  `json:"allowed_im_users"`
	AllowedIMSubjects     []string  `json:"allowed_im_subjects"`
	TokenBudget           int64     `json:"token_budget"`
	CostBudget            float64   `json:"cost_budget"`
	CostPerToken          float64   `json:"cost_per_token"`
	EstimatedTokensPerRun int64     `json:"estimated_tokens_per_run"`
	RateLimit             int       `json:"rate_limit"`
	RateWindowSeconds     int       `json:"rate_window_seconds"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type GovernanceRequest struct {
	TenantID        string
	AgentAppID      string
	UserID          string
	SessionID       string
	RequestID       string
	Channel         string
	ExternalSubject string
	Input           string
	RequiredTools   []string
	RequiredMCP     []string
}

type GovernanceResult struct {
	Input          string `json:"-"`
	TraceID        string `json:"trace_id"`
	PolicyRevision uint64 `json:"policy_revision"`
	ConfirmationID string `json:"confirmation_id,omitempty"`
}

type GovernanceCompletion struct {
	TenantID   string
	AgentAppID string
	RequestID  string
	Output     string
	Tokens     int64
	ErrorType  string
}

type GovernanceError struct {
	Code           string
	TraceID        string
	ConfirmationID string
}

func (e *GovernanceError) Error() string { return e.Code }

func IsGovernanceError(err error, code string) bool {
	var target *GovernanceError
	return errors.As(err, &target) && target.Code == code
}

type ConfirmationStatus string

const (
	ConfirmationPending  ConfirmationStatus = "pending"
	ConfirmationApproved ConfirmationStatus = "approved"
	ConfirmationRejected ConfirmationStatus = "rejected"
)

type ToolConfirmation struct {
	ID             string             `json:"id"`
	TenantID       string             `json:"tenant_id"`
	AgentAppID     string             `json:"agent_app_id"`
	SessionID      string             `json:"session_id"`
	RequestID      string             `json:"request_id"`
	UserID         string             `json:"user_id"`
	ToolName       string             `json:"tool_name"`
	PolicyRevision uint64             `json:"policy_revision"`
	TraceID        string             `json:"trace_id"`
	Status         ConfirmationStatus `json:"status"`
	CreatedAt      time.Time          `json:"created_at"`
	ExpiresAt      time.Time          `json:"expires_at"`
	DecidedAt      time.Time          `json:"decided_at,omitempty"`
	DecidedBy      string             `json:"decided_by,omitempty"`
}

type AuditQuery struct {
	TenantID  string
	Channel   string
	AgentName string
	Decision  string
	ErrorType string
	UserID    string
	SessionID string
	RequestID string
	TraceID   string
	From      time.Time
	To        time.Time
	Offset    int
	Limit     int
}

type TenantMetrics struct {
	TenantID         string  `json:"tenant_id"`
	Requests         int64   `json:"requests"`
	Active           int64   `json:"active_executions"`
	Completed        int64   `json:"completed_executions"`
	Failed           int64   `json:"failed_executions"`
	Denied           int64   `json:"denied_requests"`
	RateLimited      int64   `json:"rate_limited_requests"`
	Tokens           int64   `json:"tokens"`
	Cost             float64 `json:"cost"`
	ModelLatencyMS   int64   `json:"model_latency_ms"`
	ToolLatencyMS    int64   `json:"tool_latency_ms"`
	StorageLatencyMS int64   `json:"storage_latency_ms"`
	IMDelivered      int64   `json:"im_delivered"`
	IMFailed         int64   `json:"im_failed"`
}

type TraceSpan struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	OccurredAt time.Time `json:"occurred_at"`
}

type PlatformTrace struct {
	TraceID    string      `json:"trace_id"`
	TenantID   string      `json:"tenant_id"`
	RequestID  string      `json:"request_id"`
	SessionID  string      `json:"session_id"`
	AgentAppID string      `json:"agent_app_id"`
	Spans      []TraceSpan `json:"spans"`
}

type governanceExecution struct {
	traceID  string
	reserved int64
	started  time.Time
	active   bool
}

type rateWindow struct {
	started time.Time
	count   int
}

// GovernanceCenter is the server-owned policy, audit, accounting, and trace
// boundary. It is deterministic and process-local by default so adapters can
// replace its persistence without changing HTTP or runtime contracts.
type GovernanceCenter struct {
	mu            sync.Mutex
	policies      map[string]TenantPolicy
	audits        []AuditEvent
	confirmations map[string]ToolConfirmation
	executions    map[string]governanceExecution
	metrics       map[string]TenantMetrics
	rateWindows   map[string]rateWindow
	traces        map[string]PlatformTrace
	usedTokens    map[string]int64
	now           func() time.Time
	path          string
}

type governanceSnapshot struct {
	Policies      map[string]TenantPolicy     `json:"policies"`
	Audits        []AuditEvent                `json:"audits"`
	Confirmations map[string]ToolConfirmation `json:"confirmations"`
	Metrics       map[string]TenantMetrics    `json:"metrics"`
	UsedTokens    map[string]int64            `json:"used_tokens"`
}

func NewGovernanceCenter() *GovernanceCenter {
	return &GovernanceCenter{
		policies: map[string]TenantPolicy{}, confirmations: map[string]ToolConfirmation{}, executions: map[string]governanceExecution{},
		metrics: map[string]TenantMetrics{}, rateWindows: map[string]rateWindow{}, traces: map[string]PlatformTrace{}, usedTokens: map[string]int64{}, now: time.Now,
	}
}

func NewPersistentGovernanceCenter(path string) (*GovernanceCenter, error) {
	center := NewGovernanceCenter()
	center.path = path
	if path == "" {
		return center, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return center, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot governanceSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Policies != nil {
		center.policies = snapshot.Policies
	}
	if snapshot.Audits != nil {
		center.audits = snapshot.Audits
	}
	if snapshot.Confirmations != nil {
		center.confirmations = snapshot.Confirmations
	}
	if snapshot.Metrics != nil {
		center.metrics = snapshot.Metrics
	}
	if snapshot.UsedTokens != nil {
		center.usedTokens = snapshot.UsedTokens
	}
	return center, nil
}

func governanceKey(tenantID, appID string) string    { return tenantID + "\x00" + appID }
func executionKey(tenantID, requestID string) string { return tenantID + "\x00" + requestID }

func (g *GovernanceCenter) PutPolicy(ctx context.Context, policy TenantPolicy) (TenantPolicy, error) {
	if err := ctx.Err(); err != nil {
		return TenantPolicy{}, err
	}
	if policy.TenantID == "" || policy.AgentAppID == "" || policy.TokenBudget < 0 || policy.CostBudget < 0 || policy.CostPerToken < 0 || policy.EstimatedTokensPerRun < 0 || policy.RateLimit < 0 || policy.RateWindowSeconds < 0 {
		return TenantPolicy{}, errors.New("invalid_policy")
	}
	policy.AllowedTools = normalizedValues(policy.AllowedTools)
	policy.AllowedMCP = normalizedValues(policy.AllowedMCP)
	policy.DangerousTools = normalizedValues(policy.DangerousTools)
	policy.AllowedIMUsers = normalizedValues(policy.AllowedIMUsers)
	policy.AllowedIMSubjects = normalizedValues(policy.AllowedIMSubjects)
	policy.DeniedInputPatterns = normalizedPatterns(policy.DeniedInputPatterns)
	policy.DeniedOutputPatterns = normalizedPatterns(policy.DeniedOutputPatterns)
	policy.RedactedPatterns = normalizedPatterns(policy.RedactedPatterns)
	if !subset(policy.DangerousTools, policy.AllowedTools) {
		return TenantPolicy{}, errors.New("invalid_policy")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	previous := g.policies[governanceKey(policy.TenantID, policy.AgentAppID)]
	policy.Revision = previous.Revision + 1
	policy.UpdatedAt = g.now().UTC()
	g.policies[governanceKey(policy.TenantID, policy.AgentAppID)] = clonePolicy(policy)
	traceID := newTraceID()
	g.appendAuditLocked(AuditEvent{TenantID: policy.TenantID, AgentName: policy.AgentAppID, Decision: "policy.updated", TraceID: traceID, OccurredAt: policy.UpdatedAt})
	if err := g.persistLocked(); err != nil {
		return TenantPolicy{}, err
	}
	return clonePolicy(policy), nil
}

func (g *GovernanceCenter) Policy(tenantID, appID string) (TenantPolicy, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	policy, ok := g.policies[governanceKey(tenantID, appID)]
	return clonePolicy(policy), ok
}

func (g *GovernanceCenter) Evaluate(ctx context.Context, request GovernanceRequest) (GovernanceResult, error) {
	if err := ctx.Err(); err != nil {
		return GovernanceResult{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now().UTC()
	key := executionKey(request.TenantID, request.RequestID)
	prior, seen := g.executions[key]
	traceID := prior.traceID
	if traceID == "" {
		traceID = confirmationTraceForRequestLocked(g.confirmations, request.TenantID, request.RequestID)
	}
	if traceID == "" {
		traceID = newTraceID()
	}
	result := GovernanceResult{Input: request.Input, TraceID: traceID}
	policy, configured := g.policies[governanceKey(request.TenantID, request.AgentAppID)]
	result.PolicyRevision = policy.Revision
	g.recordTraceLocked(request, traceID, "gateway.receive", "ok", now)
	if !seen {
		metrics := g.metrics[request.TenantID]
		metrics.TenantID = request.TenantID
		metrics.Requests++
		g.metrics[request.TenantID] = metrics
	}
	deny := func(code, decision string) (GovernanceResult, error) {
		metrics := g.metrics[request.TenantID]
		metrics.Denied++
		if code == "tenant_rate_limited" {
			metrics.RateLimited++
		}
		g.metrics[request.TenantID] = metrics
		g.appendAuditLocked(auditForRequest(request, traceID, decision, code, now))
		g.recordTraceLocked(request, traceID, "policy.evaluate", "error", now)
		if err := g.persistLocked(); err != nil {
			return result, &GovernanceError{Code: "audit_unavailable", TraceID: traceID}
		}
		return result, &GovernanceError{Code: code, TraceID: traceID, ConfirmationID: result.ConfirmationID}
	}
	if !configured {
		if seen && !prior.active {
			return result, nil
		}
		g.allowLocked(request, result, key, now, 0)
		return result, nil
	}
	if seen && !prior.active {
		result.Input = redact(request.Input, policy.RedactedPatterns)
		return result, nil
	}
	for _, required := range request.RequiredTools {
		if !contains(policy.AllowedTools, required) {
			return deny("tool_not_allowed", "tool.denied")
		}
		g.appendAuditLocked(AuditEvent{TenantID: request.TenantID, Channel: request.Channel, UserID: request.UserID, SessionID: request.SessionID, AgentName: request.AgentAppID, ToolName: required, Decision: "tool.allowed", TraceID: traceID, RequestID: request.RequestID, OccurredAt: now})
		g.recordTraceLocked(request, traceID, "tool.authorize", "ok", now)
	}
	for _, required := range request.RequiredMCP {
		if !contains(policy.AllowedMCP, required) {
			return deny("mcp_not_allowed", "mcp.denied")
		}
		g.appendAuditLocked(AuditEvent{TenantID: request.TenantID, Channel: request.Channel, UserID: request.UserID, SessionID: request.SessionID, AgentName: request.AgentAppID, ToolName: "mcp:" + required, Decision: "mcp.allowed", TraceID: traceID, RequestID: request.RequestID, OccurredAt: now})
	}
	for _, pattern := range policy.DeniedInputPatterns {
		if strings.Contains(request.Input, pattern) {
			return deny("policy_denied", "guardrail.input.denied")
		}
	}
	if request.Channel != "" {
		if len(policy.AllowedIMUsers) > 0 && !contains(policy.AllowedIMUsers, request.UserID) {
			return deny("im_user_denied", "im_user.denied")
		}
		if len(policy.AllowedIMSubjects) > 0 && !contains(policy.AllowedIMSubjects, request.ExternalSubject) {
			return deny("im_subject_denied", "im_subject.denied")
		}
	}
	for _, required := range request.RequiredTools {
		if !contains(policy.DangerousTools, required) {
			continue
		}
		confirmationID := "confirmation-" + stableID(request.TenantID+"\x00"+request.RequestID+"\x00"+required)
		confirmation, ok := g.confirmations[confirmationID]
		if !ok {
			confirmation = ToolConfirmation{ID: confirmationID, TenantID: request.TenantID, AgentAppID: request.AgentAppID, SessionID: request.SessionID, RequestID: request.RequestID, UserID: request.UserID, ToolName: required, PolicyRevision: policy.Revision, TraceID: traceID, Status: ConfirmationPending, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
			g.confirmations[confirmationID] = confirmation
		}
		result.ConfirmationID = confirmationID
		if confirmation.Status == ConfirmationRejected || (!now.Before(confirmation.ExpiresAt) && confirmation.Status == ConfirmationPending) {
			return deny("confirmation_rejected", "tool.confirmation.rejected")
		}
		if confirmation.Status != ConfirmationApproved {
			return deny("confirmation_required", "tool.confirmation.pending")
		}
	}
	if prior.active {
		result.Input = redact(request.Input, policy.RedactedPatterns)
		result.ConfirmationID = confirmationForRequestLocked(g.confirmations, request.TenantID, request.RequestID)
		return result, nil
	}
	windowSeconds := policy.RateWindowSeconds
	if windowSeconds == 0 {
		windowSeconds = 60
	}
	if policy.RateLimit > 0 {
		window := g.rateWindows[request.TenantID]
		if window.started.IsZero() || now.Sub(window.started) >= time.Duration(windowSeconds)*time.Second {
			window = rateWindow{started: now}
		}
		if window.count >= policy.RateLimit {
			return deny("tenant_rate_limited", "rate_limit.denied")
		}
		window.count++
		g.rateWindows[request.TenantID] = window
	}
	reserved := policy.EstimatedTokensPerRun
	if reserved <= 0 {
		reserved = estimateTokens(request.Input)
	}
	used := g.usedTokens[request.TenantID]
	reservedTotal := int64(0)
	for existingKey, execution := range g.executions {
		if strings.HasPrefix(existingKey, request.TenantID+"\x00") && execution.active {
			reservedTotal += execution.reserved
		}
	}
	if policy.TokenBudget > 0 && used+reservedTotal+reserved > policy.TokenBudget {
		return deny("budget_exceeded", "budget.denied")
	}
	if policy.CostBudget > 0 && float64(used+reservedTotal+reserved)*policy.CostPerToken > policy.CostBudget {
		return deny("budget_exceeded", "budget.denied")
	}
	result.Input = redact(request.Input, policy.RedactedPatterns)
	g.allowLocked(request, result, key, now, reserved)
	return result, nil
}

func (g *GovernanceCenter) allowLocked(request GovernanceRequest, result GovernanceResult, key string, now time.Time, reserved int64) {
	g.executions[key] = governanceExecution{traceID: result.TraceID, reserved: reserved, started: now, active: true}
	metrics := g.metrics[request.TenantID]
	metrics.TenantID = request.TenantID
	metrics.Active++
	g.metrics[request.TenantID] = metrics
	g.appendAuditLocked(auditForRequest(request, result.TraceID, "policy.allowed", "", now))
	g.recordTraceLocked(request, result.TraceID, "policy.evaluate", "ok", now)
	_ = g.persistLocked()
}

func (g *GovernanceCenter) Complete(ctx context.Context, completion GovernanceCompletion) string {
	if ctx.Err() != nil {
		return completion.Output
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := executionKey(completion.TenantID, completion.RequestID)
	execution, ok := g.executions[key]
	if !ok || !execution.active {
		return completion.Output
	}
	execution.active = false
	g.executions[key] = execution
	policy := g.policies[governanceKey(completion.TenantID, completion.AgentAppID)]
	output := completion.Output
	for _, pattern := range policy.DeniedOutputPatterns {
		if strings.Contains(output, pattern) {
			output = "[REDACTED]"
			completion.ErrorType = "output_guardrail"
			break
		}
	}
	output = redact(output, policy.RedactedPatterns)
	tokens := completion.Tokens
	if tokens <= 0 {
		tokens = estimateTokens(output)
	}
	g.usedTokens[completion.TenantID] += tokens
	metrics := g.metrics[completion.TenantID]
	if metrics.Active > 0 {
		metrics.Active--
	}
	metrics.Tokens += tokens
	metrics.Cost += float64(tokens) * policy.CostPerToken
	metrics.ModelLatencyMS += g.now().Sub(execution.started).Milliseconds()
	decision := "run.completed"
	if completion.ErrorType != "" {
		metrics.Failed++
		decision = "run.failed"
	} else {
		metrics.Completed++
	}
	g.metrics[completion.TenantID] = metrics
	request := GovernanceRequest{TenantID: completion.TenantID, AgentAppID: completion.AgentAppID, RequestID: completion.RequestID}
	audit := auditForRequest(request, execution.traceID, decision, completion.ErrorType, g.now().UTC())
	audit.Cost = float64(tokens) * policy.CostPerToken
	audit.Latency = g.now().Sub(execution.started)
	g.appendAuditLocked(audit)
	g.recordTraceLocked(request, execution.traceID, "runner.complete", map[bool]string{true: "error", false: "ok"}[completion.ErrorType != ""], g.now().UTC())
	_ = g.persistLocked()
	return output
}

func (g *GovernanceCenter) FilterOutput(tenantID, appID, output string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	policy := g.policies[governanceKey(tenantID, appID)]
	for _, pattern := range policy.DeniedOutputPatterns {
		if strings.Contains(output, pattern) {
			return "[REDACTED]", true
		}
	}
	return redact(output, policy.RedactedPatterns), false
}

func (g *GovernanceCenter) DecideConfirmation(ctx context.Context, tenantID, id, decidedBy string, approve bool) (ToolConfirmation, error) {
	if err := ctx.Err(); err != nil {
		return ToolConfirmation{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	confirmation, ok := g.confirmations[id]
	if !ok || confirmation.TenantID != tenantID {
		return ToolConfirmation{}, ErrNotFound
	}
	if confirmation.Status != ConfirmationPending {
		return confirmation, nil
	}
	if !g.now().Before(confirmation.ExpiresAt) {
		confirmation.Status = ConfirmationRejected
	} else if approve {
		confirmation.Status = ConfirmationApproved
	} else {
		confirmation.Status = ConfirmationRejected
	}
	confirmation.DecidedAt = g.now().UTC()
	confirmation.DecidedBy = decidedBy
	g.confirmations[id] = confirmation
	g.appendAuditLocked(AuditEvent{TenantID: tenantID, UserID: decidedBy, SessionID: confirmation.SessionID, AgentName: confirmation.AgentAppID, ToolName: confirmation.ToolName, Decision: "tool.confirmation." + string(confirmation.Status), TraceID: confirmation.TraceID, RequestID: confirmation.RequestID, OccurredAt: confirmation.DecidedAt})
	if err := g.persistLocked(); err != nil {
		return ToolConfirmation{}, err
	}
	return confirmation, nil
}

func (g *GovernanceCenter) Confirmations(tenantID string) []ToolConfirmation {
	g.mu.Lock()
	defer g.mu.Unlock()
	result := []ToolConfirmation{}
	for _, item := range g.confirmations {
		if item.TenantID == tenantID {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (g *GovernanceCenter) AuditEvents(query AuditQuery) []AuditEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	result := []AuditEvent{}
	for index := len(g.audits) - 1; index >= 0 && len(result) < limit; index-- {
		event := g.audits[index]
		if query.TenantID != "" && event.TenantID != query.TenantID || query.Channel != "" && event.Channel != query.Channel || query.AgentName != "" && event.AgentName != query.AgentName || query.Decision != "" && event.Decision != query.Decision || query.ErrorType != "" && event.ErrorType != query.ErrorType || query.UserID != "" && event.UserID != query.UserID || query.SessionID != "" && event.SessionID != query.SessionID || query.RequestID != "" && event.RequestID != query.RequestID || query.TraceID != "" && event.TraceID != query.TraceID || !query.From.IsZero() && event.OccurredAt.Before(query.From) || !query.To.IsZero() && event.OccurredAt.After(query.To) {
			continue
		}
		if query.Offset > 0 {
			query.Offset--
			continue
		}
		result = append(result, event)
	}
	return result
}

func (g *GovernanceCenter) Metrics(tenantID string) TenantMetrics {
	g.mu.Lock()
	defer g.mu.Unlock()
	metrics := g.metrics[tenantID]
	metrics.TenantID = tenantID
	return metrics
}
func (g *GovernanceCenter) Trace(tenantID, traceID, requestID string) (PlatformTrace, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if traceID != "" {
		trace, ok := g.traces[traceID]
		if ok && trace.TenantID == tenantID {
			return cloneTrace(trace), true
		}
	}
	for _, trace := range g.traces {
		if trace.TenantID == tenantID && requestID != "" && trace.RequestID == requestID {
			return cloneTrace(trace), true
		}
	}
	return PlatformTrace{}, false
}

func (g *GovernanceCenter) RecordSpan(request GovernanceRequest, traceID, name, status string) {
	if traceID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recordTraceLocked(request, traceID, name, status, g.now().UTC())
}

func (g *GovernanceCenter) RecordDelivery(tenantID string, delivered bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	metrics := g.metrics[tenantID]
	metrics.TenantID = tenantID
	if delivered {
		metrics.IMDelivered++
	} else {
		metrics.IMFailed++
	}
	g.metrics[tenantID] = metrics
	_ = g.persistLocked()
}

func (g *GovernanceCenter) appendAuditLocked(event AuditEvent) {
	if event.ID == "" {
		event.ID = "audit-" + stableID(event.TenantID+event.TraceID+event.Decision+event.OccurredAt.String())
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = g.now().UTC()
	}
	g.audits = append(g.audits, event)
	if len(g.audits) > 10000 {
		g.audits = append([]AuditEvent(nil), g.audits[len(g.audits)-10000:]...)
	}
}

func (g *GovernanceCenter) Record(ctx context.Context, event AuditEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.appendAuditLocked(event)
	return g.persistLocked()
}

func (g *GovernanceCenter) persistLocked() error {
	if g.path == "" {
		return nil
	}
	snapshot := governanceSnapshot{Policies: g.policies, Audits: g.audits, Confirmations: g.confirmations, Metrics: g.metrics, UsedTokens: g.usedTokens}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0700); err != nil {
		return err
	}
	temporary := g.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, g.path)
}
func (g *GovernanceCenter) recordTraceLocked(request GovernanceRequest, traceID, name, status string, now time.Time) {
	trace := g.traces[traceID]
	trace.TraceID = traceID
	trace.TenantID = request.TenantID
	trace.RequestID = request.RequestID
	if request.SessionID != "" {
		trace.SessionID = request.SessionID
	}
	if request.AgentAppID != "" {
		trace.AgentAppID = request.AgentAppID
	}
	trace.Spans = append(trace.Spans, TraceSpan{Name: name, Status: status, OccurredAt: now})
	g.traces[traceID] = trace
}

func auditForRequest(request GovernanceRequest, traceID, decision, errorType string, now time.Time) AuditEvent {
	return AuditEvent{TenantID: request.TenantID, Channel: request.Channel, UserID: request.UserID, SessionID: request.SessionID, AgentName: request.AgentAppID, Decision: decision, ErrorType: errorType, TraceID: traceID, RequestID: request.RequestID, OccurredAt: now}
}
func estimateTokens(value string) int64 {
	n := int64(len([]rune(value)))
	if n == 0 {
		return 1
	}
	return (n + 3) / 4
}
func redact(value string, patterns []string) string {
	for _, pattern := range patterns {
		value = strings.ReplaceAll(value, pattern, "[REDACTED]")
	}
	return value
}
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func subset(values, allowed []string) bool {
	for _, value := range values {
		if !contains(allowed, value) {
			return false
		}
	}
	return true
}
func normalizedValues(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
func normalizedPatterns(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
func clonePolicy(policy TenantPolicy) TenantPolicy {
	policy.AllowedTools = append([]string{}, policy.AllowedTools...)
	policy.AllowedMCP = append([]string{}, policy.AllowedMCP...)
	policy.DangerousTools = append([]string{}, policy.DangerousTools...)
	policy.DeniedInputPatterns = append([]string{}, policy.DeniedInputPatterns...)
	policy.DeniedOutputPatterns = append([]string{}, policy.DeniedOutputPatterns...)
	policy.RedactedPatterns = append([]string{}, policy.RedactedPatterns...)
	policy.AllowedIMUsers = append([]string{}, policy.AllowedIMUsers...)
	policy.AllowedIMSubjects = append([]string{}, policy.AllowedIMSubjects...)
	return policy
}
func cloneTrace(trace PlatformTrace) PlatformTrace {
	trace.Spans = append([]TraceSpan(nil), trace.Spans...)
	return trace
}
func newTraceID() string {
	bytes := make([]byte, 16)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
func stableID(value string) string      { sum := sha256Bytes(value); return hex.EncodeToString(sum[:8]) }
func sha256Bytes(value string) [32]byte { return sha256.Sum256([]byte(value)) }
func confirmationForRequestLocked(items map[string]ToolConfirmation, tenantID, requestID string) string {
	for _, item := range items {
		if item.TenantID == tenantID && item.RequestID == requestID {
			return item.ID
		}
	}
	return ""
}

func confirmationTraceForRequestLocked(items map[string]ToolConfirmation, tenantID, requestID string) string {
	for _, item := range items {
		if item.TenantID == tenantID && item.RequestID == requestID {
			return item.TraceID
		}
	}
	return ""
}
func traceForRequestLocked(items map[string]governanceExecution, tenantID, requestID string) string {
	return items[executionKey(tenantID, requestID)].traceID
}
