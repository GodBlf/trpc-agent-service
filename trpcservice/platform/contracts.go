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
	ID        string
	Name      string
	CreatedAt time.Time
}

type AgentApp struct {
	ID       string
	TenantID string
	Name     string
}

type DeploymentStatus string

const (
	DeploymentDraft     DeploymentStatus = "draft"
	DeploymentPublished DeploymentStatus = "published"
	DeploymentActive    DeploymentStatus = "active"
	DeploymentPaused    DeploymentStatus = "paused"
)

type Deployment struct {
	ID              string
	TenantID        string
	AgentAppID      string
	VersionID       string
	Status          DeploymentStatus
	DesiredReplicas int
}

type DeploymentVersion struct {
	ID         string
	AgentAppID string
	Number     int
	Config     map[string]any
	CreatedAt  time.Time
}

type GatewayRequest struct {
	AppID     string
	SessionID string
	Input     string
}

type GatewayResponse struct {
	SessionID string
	Output    string
}

type Gateway interface {
	Handle(context.Context, GatewayRequest) (GatewayResponse, error)
}

type Worker interface {
	Execute(context.Context, GatewayRequest) (GatewayResponse, error)
}

type Session struct {
	ID       string
	TenantID string
	AppID    string
	UserID   string
	Sequence uint64
}

type SessionEvent struct {
	ID             string
	TenantID       string
	SessionID      string
	Sequence       uint64
	IdempotencyKey string
	Type           string
	Payload        []byte
	OccurredAt     time.Time
}

type RunnerRequest struct {
	AppID     string
	SessionID string
	Input     string
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
