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
export interface MigrationResult { id: string; status: string; dry_run: boolean; source_count: number; destination_count: number; checksum?: string; message?: string }

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
  backend: () => request<{ backend: string; health: BackendHealth }>("/api/v1/admin/storage/backend"),
  selectBackend: (backend: string, address?: string) => request<{ backend: string; health: BackendHealth }>("/api/v1/admin/storage/backend", { method: "POST", body: JSON.stringify({ backend, address }) }),
  session: (id: string) => request<SessionState>(`/api/v1/admin/sessions/${encodeURIComponent(id)}`),
  sessionEvents: (id: string) => request<ListResponse<SessionEvent>>(`/api/v1/admin/sessions/${encodeURIComponent(id)}/events`),
  memory: (id: string) => request<ListResponse<MemoryRecord>>(`/api/v1/admin/memory/${encodeURIComponent(id)}`),
  migrate: (dry_run: boolean) => request<MigrationResult>("/api/v1/admin/migrations", { method: "POST", body: JSON.stringify({ dry_run }) }),
};
