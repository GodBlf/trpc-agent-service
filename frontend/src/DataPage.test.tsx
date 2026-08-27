import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { vi } from "vitest";
import { DataPage } from "./DataPage";

test("shows backend health and starts a migration dry-run", async () => {
  const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
    const path = String(input);
    if (path.endsWith("/storage/backend")) return new Response(JSON.stringify({ backend: "inmemory", health: { backend: "inmemory", status: "healthy", checked_at: "now" } }), { status: 200 });
    if (path.includes("/sessions/") && path.endsWith("/events")) return new Response(JSON.stringify({ items: [] }), { status: 200 });
    if (path.includes("/sessions/")) return new Response(JSON.stringify({ error: { code: "session_not_found", message: "missing" } }), { status: 404 });
    if (path.includes("/memory/")) return new Response(JSON.stringify({ items: [] }), { status: 200 });
    if (path.endsWith("/migrations") && init?.method === "POST") return new Response(JSON.stringify({ id: "migration-1", status: "completed", dry_run: true, source_count: 0, destination_count: 0 }), { status: 202 });
    throw new Error(`unexpected request ${path}`);
  });
  render(<DataPage identity={{ id: "admin", name: "Admin", active_tenant_id: "tenant-a", active_role: "platform_admin", assignments: [] }} />);
  expect(await screen.findByText("healthy")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /Dry-run/ }));
  await waitFor(() => expect(screen.getByText(/迁移 completed/)).toBeInTheDocument());
  fetchMock.mockRestore();
});
