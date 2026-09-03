package platform

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGovernanceCenterPersistsPoliciesAndAuditEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "governance.json")
	center, err := NewPersistentGovernanceCenter(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = center.PutPolicy(context.Background(), TenantPolicy{TenantID: "tenant-a", AgentAppID: "app-a", AllowedTools: []string{"search"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := center.Record(context.Background(), AuditEvent{TenantID: "tenant-a", Decision: "authorization.denied", TraceID: "trace-a"}); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewPersistentGovernanceCenter(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, found := reloaded.Policy("tenant-a", "app-a")
	if !found || policy.Revision != 1 || len(policy.AllowedTools) != 1 {
		t.Fatalf("policy = %#v, found = %v", policy, found)
	}
	audits := reloaded.AuditEvents(AuditQuery{TenantID: "tenant-a", Limit: 10})
	if len(audits) != 2 || audits[0].Decision != "authorization.denied" {
		t.Fatalf("audits = %#v", audits)
	}
}

func TestGovernanceCenterEnforcesPolicyBeforeExecutionAndRecordsAudit(t *testing.T) {
	center := NewGovernanceCenter()
	policy, err := center.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-a", AgentAppID: "app-a", AllowedTools: []string{"search"}, AllowedMCP: []string{"calendar"},
		DeniedInputPatterns: []string{"blocked"}, RedactedPatterns: []string{"canary-secret"},
		AllowedIMUsers: []string{"user-a"}, AllowedIMSubjects: []string{"chat-a"},
		TokenBudget: 100, CostBudget: 1, CostPerToken: 0.01, EstimatedTokensPerRun: 10, RateLimit: 2, RateWindowSeconds: 60,
	})
	if err != nil || policy.Revision != 1 {
		t.Fatalf("policy = %#v, err = %v", policy, err)
	}

	denied, err := center.Evaluate(context.Background(), GovernanceRequest{
		TenantID: "tenant-a", AgentAppID: "app-a", UserID: "user-a", SessionID: "session-a", RequestID: "request-denied",
		Input: "this is blocked", RequiredTools: []string{"search"},
	})
	var governanceErr *GovernanceError
	if !errors.As(err, &governanceErr) || governanceErr.Code != "policy_denied" || denied.TraceID == "" {
		t.Fatalf("result = %#v, err = %v", denied, err)
	}

	allowed, err := center.Evaluate(context.Background(), GovernanceRequest{
		TenantID: "tenant-a", AgentAppID: "app-a", UserID: "user-a", SessionID: "session-a", RequestID: "request-allowed",
		Channel: ChannelTelegram, ExternalSubject: "chat-a", Input: "hello canary-secret", RequiredTools: []string{"search"}, RequiredMCP: []string{"calendar"},
	})
	if err != nil || allowed.Input != "hello [REDACTED]" || allowed.TraceID == "" {
		t.Fatalf("result = %#v, err = %v", allowed, err)
	}
	center.Complete(context.Background(), GovernanceCompletion{TenantID: "tenant-a", AgentAppID: "app-a", RequestID: "request-allowed", Output: "done", Tokens: 4})

	audits := center.AuditEvents(AuditQuery{TenantID: "tenant-a", Limit: 20})
	if len(audits) < 3 {
		t.Fatalf("audit events = %#v", audits)
	}
	for _, event := range audits {
		if event.TenantID != "tenant-a" || event.TraceID == "" {
			t.Fatalf("audit event = %#v", event)
		}
	}
	metrics := center.Metrics("tenant-a")
	if metrics.Requests != 2 || metrics.Denied != 1 || metrics.Completed != 1 || metrics.Tokens != 4 || metrics.Cost != 0.04 {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestGovernanceCenterBlocksOutputBeforeCompletionAudit(t *testing.T) {
	center := NewGovernanceCenter()
	_, err := center.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-a", AgentAppID: "app-a", DeniedOutputPatterns: []string{"forbidden-output"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = center.Evaluate(context.Background(), GovernanceRequest{
		TenantID: "tenant-a", AgentAppID: "app-a", SessionID: "session-a", RequestID: "request-a", Input: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	output := center.Complete(context.Background(), GovernanceCompletion{
		TenantID: "tenant-a", AgentAppID: "app-a", RequestID: "request-a", Output: "contains forbidden-output",
	})
	if output != "[REDACTED]" {
		t.Fatalf("output = %q", output)
	}
	audits := center.AuditEvents(AuditQuery{TenantID: "tenant-a", RequestID: "request-a", ErrorType: "output_guardrail"})
	if len(audits) != 1 || audits[0].Decision != "run.failed" {
		t.Fatalf("audits = %#v", audits)
	}
	metrics := center.Metrics("tenant-a")
	if metrics.Failed != 1 || metrics.Active != 0 {
		t.Fatalf("metrics = %#v", metrics)
	}
}

func TestGovernanceAuditSearchFiltersAndPaginates(t *testing.T) {
	center := NewGovernanceCenter()
	base := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	for index, event := range []AuditEvent{
		{TenantID: "tenant-a", Channel: ChannelTelegram, UserID: "user-a", SessionID: "session-a", AgentName: "app-a", Decision: "policy.allowed", RequestID: "request-a", TraceID: "trace-a"},
		{TenantID: "tenant-a", Channel: ChannelEnterpriseWeChat, UserID: "user-b", SessionID: "session-b", AgentName: "app-b", Decision: "run.failed", ErrorType: "runner_failed", RequestID: "request-b", TraceID: "trace-b"},
		{TenantID: "tenant-b", Channel: ChannelTelegram, UserID: "user-a", SessionID: "session-a", AgentName: "app-a", Decision: "policy.allowed", RequestID: "request-a", TraceID: "trace-a"},
	} {
		event.OccurredAt = base.Add(time.Duration(index) * time.Minute)
		if err := center.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	audits := center.AuditEvents(AuditQuery{
		TenantID: "tenant-a", Channel: ChannelEnterpriseWeChat, AgentName: "app-b", ErrorType: "runner_failed",
		From: base.Add(30 * time.Second), To: base.Add(90 * time.Second), Limit: 1,
	})
	if len(audits) != 1 || audits[0].RequestID != "request-b" {
		t.Fatalf("filtered audits = %#v", audits)
	}
	if audits := center.AuditEvents(AuditQuery{TenantID: "tenant-a", Offset: 1, Limit: 1}); len(audits) != 1 || audits[0].RequestID != "request-a" {
		t.Fatalf("paginated audits = %#v", audits)
	}
}

func TestGovernanceCenterRequiresDangerousToolConfirmationExactlyOnce(t *testing.T) {
	center := NewGovernanceCenter()
	_, err := center.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-a", AgentAppID: "app-a", AllowedTools: []string{"deploy"}, DangerousTools: []string{"deploy"},
		TokenBudget: 100, EstimatedTokensPerRun: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", UserID: "user-a", SessionID: "session-a", RequestID: "request-a", RequiredTools: []string{"deploy"}, Input: "ship"}
	result, err := center.Evaluate(context.Background(), request)
	var governanceErr *GovernanceError
	if !errors.As(err, &governanceErr) || governanceErr.Code != "confirmation_required" || result.ConfirmationID == "" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	confirmation, err := center.DecideConfirmation(context.Background(), "tenant-a", result.ConfirmationID, "admin-a", true)
	if err != nil || confirmation.Status != ConfirmationApproved {
		t.Fatalf("confirmation = %#v, err = %v", confirmation, err)
	}
	result, err = center.Evaluate(context.Background(), request)
	if err != nil || result.ConfirmationID != confirmation.ID {
		t.Fatalf("approved result = %#v, err = %v", result, err)
	}
	second, err := center.DecideConfirmation(context.Background(), "tenant-a", confirmation.ID, "admin-a", true)
	if err != nil || second.DecidedAt != confirmation.DecidedAt {
		t.Fatalf("idempotent decision = %#v, err = %v", second, err)
	}
}

func TestGovernanceCenterEnforcesBudgetRateAndIMAuthorization(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	center := NewGovernanceCenter()
	center.now = func() time.Time { return now }
	_, _ = center.PutPolicy(context.Background(), TenantPolicy{
		TenantID: "tenant-a", AgentAppID: "app-a", AllowedIMUsers: []string{"allowed"}, TokenBudget: 2,
		EstimatedTokensPerRun: 1, RateLimit: 1, RateWindowSeconds: 60,
	})
	_, err := center.Evaluate(context.Background(), GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", Channel: ChannelTelegram, UserID: "denied", RequestID: "denied", Input: "hello"})
	if !IsGovernanceError(err, "im_user_denied") {
		t.Fatalf("IM error = %v", err)
	}
	_, err = center.Evaluate(context.Background(), GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", Channel: ChannelTelegram, UserID: "allowed", RequestID: "one", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = center.Evaluate(context.Background(), GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", Channel: ChannelTelegram, UserID: "allowed", RequestID: "two", Input: "hello"})
	if !IsGovernanceError(err, "tenant_rate_limited") {
		t.Fatalf("rate error = %v", err)
	}
	now = now.Add(time.Minute)
	_, err = center.Evaluate(context.Background(), GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", Channel: ChannelTelegram, UserID: "allowed", RequestID: "three", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	_, err = center.Evaluate(context.Background(), GovernanceRequest{TenantID: "tenant-a", AgentAppID: "app-a", Channel: ChannelTelegram, UserID: "allowed", RequestID: "four", Input: "hello"})
	if !IsGovernanceError(err, "budget_exceeded") {
		t.Fatalf("budget error = %v", err)
	}
}
