import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DeploymentsPage } from "./DeploymentsPage";
import type { Identity } from "./api";

const identity: Identity = {
  id: "admin",
  name: "Admin",
  active_tenant_id: "tenant-one",
  active_role: "tenant_admin",
  assignments: [],
};

const deployment = {
  id: "deploy-one",
  tenant_id: "tenant-one",
  agent_app_id: "app-one",
  status: "draft",
  desired_replicas: 1,
  created_at: "2026-01-01T00:00:00Z",
};

function jsonResponse(body: unknown) {
  return { ok: true, status: 200, json: async () => body };
}

test("reuses a Version idempotency key after an ambiguous failure and rotates it after success", async () => {
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse({ items: [deployment] }))
    .mockResolvedValueOnce(jsonResponse({ items: [{ id: "app-one", tenant_id: "tenant-one", name: "App One", created_at: "2026-01-01T00:00:00Z" }] }))
    .mockResolvedValueOnce(jsonResponse({ items: [] }))
    .mockRejectedValueOnce(new TypeError("connection lost"))
    .mockResolvedValueOnce(jsonResponse({ ...deployment, id: "deploy-one-v1", deployment_id: "deploy-one", agent_app_id: "app-one", number: 1, config: { runner: "fake" } }))
    .mockResolvedValueOnce(jsonResponse({ ...deployment, id: "deploy-one-v2", deployment_id: "deploy-one", agent_app_id: "app-one", number: 2, config: { runner: "other" } }));
  vi.stubGlobal("fetch", fetchMock);

  render(<DeploymentsPage identity={identity} />);
  await userEvent.click(await screen.findByText("deploy-one"));
  await userEvent.click(await screen.findByRole("button", { name: "创建版本" }));
  const config = screen.getByLabelText("JSON 配置");
  const versionForm = config.closest("form");
  if (!versionForm) throw new Error("Version form was not rendered");
  fireEvent.change(config, { target: { value: '{"runner":"fake"}' } });
  await userEvent.click(within(versionForm).getByRole("button", { name: "创建版本" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4));
  await userEvent.click(within(versionForm).getByRole("button", { name: "创建版本" }));
  await screen.findByText("v1");

  const firstKey = new Headers(fetchMock.mock.calls[3][1].headers).get("Idempotency-Key");
  const replayKey = new Headers(fetchMock.mock.calls[4][1].headers).get("Idempotency-Key");
  expect(firstKey).toBeTruthy();
  expect(replayKey).toBe(firstKey);

  await userEvent.click(screen.getByRole("button", { name: "创建版本" }));
  const nextConfig = screen.getByLabelText("JSON 配置");
  const nextVersionForm = nextConfig.closest("form");
  if (!nextVersionForm) throw new Error("Version form was not rendered");
  fireEvent.change(nextConfig, { target: { value: '{"runner":"other"}' } });
  await userEvent.click(within(nextVersionForm).getByRole("button", { name: "创建版本" }));
  await screen.findByText("v2");
  const nextKey = new Headers(fetchMock.mock.calls[5][1].headers).get("Idempotency-Key");
  expect(nextKey).toBeTruthy();
  expect(nextKey).not.toBe(firstKey);
});

test("shows an Active Deployment conflict and refreshes server-authoritative state", async () => {
  const first = { ...deployment, id: "deploy-one", status: "active", version_id: "deploy-one-v1" };
  const second = { ...deployment, id: "deploy-two", status: "published", version_id: "deploy-two-v1" };
  const fetchMock = vi.fn()
    .mockResolvedValueOnce(jsonResponse({ items: [first, second] }))
    .mockResolvedValueOnce(jsonResponse({ items: [{ id: "app-one", tenant_id: "tenant-one", name: "App One", created_at: "2026-01-01T00:00:00Z" }] }))
    .mockResolvedValueOnce(jsonResponse({ items: [{ id: "deploy-two-v1", deployment_id: "deploy-two", agent_app_id: "app-one", number: 1, config: { runner: "fake" } }] }))
    .mockResolvedValueOnce({ ok: false, status: 409, json: async () => ({ error: { code: "agent_app_already_has_active_deployment", message: "Agent App already has an active Deployment" } }) })
    .mockResolvedValueOnce(jsonResponse({ items: [first, second] }));
  vi.stubGlobal("fetch", fetchMock);

  render(<DeploymentsPage identity={identity} />);
  await userEvent.click(await screen.findByText("deploy-two"));
  await userEvent.click(await screen.findByRole("button", { name: "激活" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Agent App already has an active Deployment");
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(5));
  expect(fetchMock.mock.calls[4][0]).toBe("/api/v1/admin/deployments");
  expect(screen.getByRole("heading", { name: "deploy-two" })).toBeInTheDocument();
  expect(screen.getAllByText("active")).toHaveLength(1);
  expect(screen.getAllByText("published").length).toBeGreaterThan(0);
});
