package log

import "testing"

func TestRedactorRemovesSecretsAndSensitivePatterns(t *testing.T) {
	redactor := NewRedactor([]string{"bot-secret", "postgres://user:password@db/service"}, []string{"customer-id-42"})
	got := redactor.Redact("token=bot-secret dsn=postgres://user:password@db/service subject=customer-id-42")
	if got != "token=[REDACTED] dsn=[REDACTED] subject=[REDACTED]" {
		t.Fatalf("redacted = %q", got)
	}
	if redactor.Redact("safe diagnostic") != "safe diagnostic" {
		t.Fatal("safe diagnostic was modified")
	}
}
