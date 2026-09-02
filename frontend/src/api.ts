export type Role = "platform_admin" | "tenant_admin" | "operator" | "viewer";

export interface TenantAssignment {
  tenant_id: string;
  tenant_name: string;
  role: Role;
}

export interface Identity {
  id: string;
  name: string;
  active_tenant_id: string;
  active_role: Role;
  assignments: TenantAssignment[];
}

export interface Tenant {
  id: string;
  name: string;
  created_at: string;
}

export interface ListResponse<T> { items: T[] }
export interface AgentApp { id: string; tenant_id: string; name: string; created_at: string }
export type DeploymentStatus = "draft" | "published" | "active" | "paused";
export interface Deployment { id: string; tenant_id: string; agent_app_id: string; version_id?: string; status: DeploymentStatus; desired_replicas: number; created_at: string }
export interface DeploymentVersion { id: string; deployment_id: string; agent_app_id: string; number: number; config: Record<string, unknown>; created_at: string }
export interface RuntimeStatus { id: string; role: "gateway" | "worker"; available: boolean; lifecycle: "healthy" | "unavailable" | "closing" | "error"; active_executions: number; completed_executions: number; failed_executions: number }
export interface BackendHealth { backend: string; status: string; message?: string; checked_at: string }
export interface SessionState { id: string; tenant_id: string; sequence: number; summary: string; event_count: number; updated_at: string }
export interface SessionEvent { id: string; tenant_id: string; session_id: string; sequence: number; idempotency_key: string; type: string; payload: string; occurred_at: string }
export interface MemoryRecord { id: string; tenant_id: string; session_id: string; key: string; value: string; updated_at: string }
export interface MigrationResult { id: string; status: string; dry_run: boolean; sessions: number; processed_sessions: number; source_count: number; destination_count: number; checksum?: string; message?: string }
export interface ChatSession { id: string; tenant_id: string; app_id: string; user_id?: string; sequence?: number }
export interface ChatEvent { id: string; tenant_id: string; session_id: string; sequence: number; idempotency_key: string; type: string; payload: string; occurred_at: string }
export interface ChatRunResponse { session_id: string; request_id: string; status: "running" | "pending" | "completed" | "failed" | "cancelled" }
export interface ChatStreamEvent {
  event_id: string;
  request_id: string;
  session_id: string;
  sequence: number;
  type: "run.started" | "message.delta" | "message.completed" | "run.failed" | "run.cancelled" | "run.completed" | string;
  data: Record<string, unknown>;
}
export interface MockFaultConfiguration {
  scenario: "none" | "timeout" | "retry" | "rate_limit" | "message_length" | "attachment";
  message_length_limit: number;
  attachment_size_limit: number;
  rate_limit: number;
  timeout_ms: number;
  retry_limit: number;
}
export type ChannelProvider = "mock" | "enterprise_wechat" | "telegram";
export interface ChannelBinding {
  id: string;
  tenant_id: string;
  app_id: string;
  channel: ChannelProvider;
  conversation_type: "single" | "group";
  external_conversation_id: string;
  external_user_id: string;
  session_id: string;
  enabled: boolean;
  created_at: string;
  secret?: string;
}
export interface ProviderStatus { provider: string; status: string; last_error?: string }
export interface BotRoute { provider: "enterprise_wechat" | "telegram"; external_subject: string; tenant_id: string; app_id: string; conversation_type: "single" | "group" }

export class APIError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (response.status === 204) return undefined as T;
  const body = (await response.json()) as T | { error: { code: string; message: string } };
  if (!response.ok) {
    const error = (body as { error?: { code?: string; message?: string } }).error;
    throw new APIError(response.status, error?.code ?? "unknown_error", error?.message ?? "服务请求失败");
  }
  return body as T;
}

export const api = {
  identity: () => request<Identity>("/api/v1/auth/me"),
  switchTenant: (tenant_id: string) =>
    request<Identity>("/api/v1/auth/switch-tenant", {
      method: "POST",
      body: JSON.stringify({ tenant_id }),
    }),
  tenants: () => request<ListResponse<Tenant>>("/api/v1/admin/tenants"),
  tenant: (id: string) => request<Tenant>(`/api/v1/admin/tenants/${encodeURIComponent(id)}`),
  createTenant: (input: { id: string; name: string }) =>
    request<Tenant>("/api/v1/admin/tenants", { method: "POST", body: JSON.stringify(input) }),
  apps: () => request<ListResponse<AgentApp>>("/api/v1/admin/agent-apps"),
  createApp: (input: { id: string; name: string }) => request<AgentApp>("/api/v1/admin/agent-apps", { method: "POST", body: JSON.stringify(input) }),
  deployments: () => request<ListResponse<Deployment>>("/api/v1/admin/deployments"),
  createDeployment: (input: { id: string; agent_app_id: string }) => request<Deployment>("/api/v1/admin/deployments", { method: "POST", body: JSON.stringify(input) }),
  versions: (id: string) => request<ListResponse<DeploymentVersion>>(`/api/v1/admin/deployments/${encodeURIComponent(id)}/versions`),
  createVersion: (id: string, config: Record<string, unknown>, idempotencyKey: string) => request<DeploymentVersion>(`/api/v1/admin/deployments/${encodeURIComponent(id)}/versions`, { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: JSON.stringify({ config }) }),
  transition: (id: string, status: DeploymentStatus, version_id?: string) => request<Deployment>(`/api/v1/admin/deployments/${encodeURIComponent(id)}/transition`, { method: "POST", body: JSON.stringify({ status, version_id }) }),
  runtimeStatus: () => request<ListResponse<RuntimeStatus>>("/api/v1/admin/runtime/status"),
  backend: () => request<{ backend: string; health: BackendHealth; available_backends: string[] }>("/api/v1/admin/storage/backend"),
  selectBackend: (backend: string) => request<{ backend: string; health: BackendHealth }>("/api/v1/admin/storage/backend", { method: "POST", body: JSON.stringify({ backend }) }),
  session: (id: string) => request<SessionState>(`/api/v1/admin/sessions/${encodeURIComponent(id)}`),
  sessionEvents: (id: string) => request<ListResponse<SessionEvent>>(`/api/v1/admin/sessions/${encodeURIComponent(id)}/events`),
  memory: (id: string) => request<ListResponse<MemoryRecord>>(`/api/v1/admin/memory/${encodeURIComponent(id)}`),
  migrate: (input: { dry_run: boolean; batch_size: number }) => request<MigrationResult>("/api/v1/admin/migrations", { method: "POST", body: JSON.stringify(input) }),
  migration: (id: string) => request<MigrationResult>(`/api/v1/admin/migrations/${encodeURIComponent(id)}`),
  createChatSession: (app_id: string, session_id: string) =>
    request<ChatSession>("/api/v1/chat/sessions", { method: "POST", body: JSON.stringify({ app_id, session_id }) }),
  chatEvents: (id: string) => request<ListResponse<ChatEvent>>(`/api/v1/chat/sessions/${encodeURIComponent(id)}/events`),
  sendChatMessage: (id: string, input: string, requestID: string) =>
    request<ChatRunResponse>(`/api/v1/chat/sessions/${encodeURIComponent(id)}/messages`, {
      method: "POST", headers: { "X-Request-ID": requestID }, body: JSON.stringify({ input }),
    }),
  cancelChatRun: (id: string, requestID: string) =>
    request<ChatRunResponse>(`/api/v1/chat/sessions/${encodeURIComponent(id)}/cancel`, {
      method: "POST", body: JSON.stringify({ request_id: requestID }),
    }),
  mockFaults: (id: string) => request<MockFaultConfiguration>(`/api/v1/chat/mock/faults?session_id=${encodeURIComponent(id)}`),
  setMockFaults: (id: string, scenario: MockFaultConfiguration["scenario"]) =>
    request<MockFaultConfiguration>("/api/v1/chat/mock/faults", {
      method: "POST", body: JSON.stringify({ scenario, session_id: id }),
    }),
  bindings: () => request<ListResponse<ChannelBinding>>("/api/v1/chat/bindings"),
  createBinding: (input: { channel: ChannelProvider; app_id: string; conversation_type: "single" | "group"; external_conversation_id: string; external_user_id: string }) =>
    request<ChannelBinding>("/api/v1/chat/bindings", { method: "POST", body: JSON.stringify(input) }),
  setBindingEnabled: (id: string, enabled: boolean) => request<ChannelBinding>(`/api/v1/chat/bindings/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ enabled }) }),
  deleteBinding: (id: string) => request<void>(`/api/v1/chat/bindings/${encodeURIComponent(id)}`, { method: "DELETE" }),
  providerStatuses: () => request<ListResponse<ProviderStatus>>("/api/v1/admin/providers/status"),
  providerRoutes: () => request<ListResponse<BotRoute>>("/api/v1/admin/providers/routes"),
  createProviderRoute: (route: BotRoute) => request<BotRoute>("/api/v1/admin/providers/routes", { method: "POST", body: JSON.stringify(route) }),
};
