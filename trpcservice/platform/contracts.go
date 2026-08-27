// Package platform contains the stable domain and adapter contracts shared by
// the gateway, workers, channels, and storage implementations.
package platform

import (
	"context"
	"errors"
	"time"
)

// TenantContext is the trusted tenant identity attached by an ingress
// middleware. It is deliberately not constructible from a client request.
type TenantContext struct {
	TenantID string
	UserID   string
	Role     Role
}

// TenantIDFromContext is a convenience for ports that only need the boundary
// identifier. The boolean is false when middleware did not establish trust.
func TenantIDFromContext(ctx context.Context) (string, bool) {
	tenant, ok := TenantContextFromContext(ctx)
	return tenant.TenantID, ok
}

type tenantContextKey struct{}

// WithTenantContext is intended for trusted server middleware only.
func WithTenantContext(ctx context.Context, tenant TenantContext) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

// TenantContextFromContext returns the server-injected tenant identity.
func TenantContextFromContext(ctx context.Context) (TenantContext, bool) {
	if ctx == nil {
		return TenantContext{}, false
	}
	value, ok := ctx.Value(tenantContextKey{}).(TenantContext)
	return value, ok && value.TenantID != ""
}

type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentApp struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type DeploymentStatus string

const (
	DeploymentDraft     DeploymentStatus = "draft"
	DeploymentPublished DeploymentStatus = "published"
	DeploymentActive    DeploymentStatus = "active"
	DeploymentPaused    DeploymentStatus = "paused"
)

type Deployment struct {
	ID              string           `json:"id"`
	TenantID        string           `json:"tenant_id"`
	AgentAppID      string           `json:"agent_app_id"`
	VersionID       string           `json:"version_id,omitempty"`
	Status          DeploymentStatus `json:"status"`
	DesiredReplicas int              `json:"desired_replicas"`
	CreatedAt       time.Time        `json:"created_at"`
}

type DeploymentVersion struct {
	ID           string         `json:"id"`
	TenantID     string         `json:"tenant_id"`
	AgentAppID   string         `json:"agent_app_id"`
	DeploymentID string         `json:"deployment_id"`
	Number       int            `json:"number"`
	Config       map[string]any `json:"config"`
	CreatedAt    time.Time      `json:"created_at"`
}

type GatewayRequest struct {
	AppID        string `json:"app_id"`
	SessionID    string `json:"session_id"`
	Input        string `json:"input"`
	DeploymentID string `json:"-"`
	VersionID    string `json:"-"`
}

type GatewayResponse struct {
	SessionID string `json:"session_id"`
	Output    string `json:"output"`
}

type Gateway interface {
	Handle(context.Context, GatewayRequest) (GatewayResponse, error)
}

type Worker interface {
	Execute(context.Context, GatewayRequest) (GatewayResponse, error)
}

type Session struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	AppID    string `json:"app_id,omitempty"`
	UserID   string `json:"user_id,omitempty"`
	Sequence uint64 `json:"sequence"`
}

type SessionEvent struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	SessionID      string    `json:"session_id"`
	Sequence       uint64    `json:"sequence"`
	IdempotencyKey string    `json:"idempotency_key"`
	Type           string    `json:"type"`
	Payload        []byte    `json:"payload"`
	OccurredAt     time.Time `json:"occurred_at"`
}

type RunnerRequest struct {
	AppID        string
	SessionID    string
	Input        string
	DeploymentID string
	VersionID    string
}

type RunnerResponse struct {
	Output string
}

// RunnerAdapter is the platform boundary around trpc-agent-go's runner.Runner.
type RunnerAdapter interface {
	Run(context.Context, RunnerRequest) (RunnerResponse, error)
}

type ChannelMessage struct {
	AppID      string
	SessionID  string
	MessageID  string
	UserID     string
	Text       string
	ReceivedAt time.Time
}

type ChannelReply struct {
	MessageID string
	Text      string
}

// ChannelAdapter converts an external IM callback into platform messages and
// sends replies back through the same binding.
type ChannelAdapter interface {
	Receive(context.Context, []byte) (ChannelMessage, error)
	Send(context.Context, ChannelReply) error
}

// StorageAdapter is intentionally small: concrete backends may add specialized
// Memory, Summary, Artifact, or Knowledge methods without changing this core.
type StorageAdapter interface {
	GetSession(context.Context, string, string) (Session, error)
	AppendSessionEvent(context.Context, SessionEvent) error
	ListSessionEvents(context.Context, string, string, uint64) ([]SessionEvent, error)
}

type AuditEvent struct {
	ID         string
	TenantID   string
	Channel    string
	UserID     string
	SessionID  string
	AgentName  string
	ToolName   string
	Decision   string
	Latency    time.Duration
	ErrorType  string
	Cost       float64
	TraceID    string
	OccurredAt time.Time
}

type AuditSink interface {
	Record(context.Context, AuditEvent) error
}

var (
	ErrNotFound       = errors.New("platform: not found")
	ErrDuplicateEvent = errors.New("platform: duplicate session event")
)
