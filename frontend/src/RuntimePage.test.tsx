import { render, screen, waitFor } from "@testing-library/react";
import { RuntimePage } from "./RuntimePage";
import type { RuntimeStatus } from "./api";

test("renders backend-reported gateway and worker state without request data", async () => {
  const items: RuntimeStatus[] = [
    { id: "gateway-local", role: "gateway", available: true, lifecycle: "healthy", active_executions: 0, completed_executions: 2, failed_executions: 0 },
    { id: "worker-local", role: "worker", available: false, lifecycle: "error", active_executions: 0, completed_executions: 2, failed_executions: 1 },
  ];
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items }) }));
  render(<RuntimePage />);
  await waitFor(() => expect(screen.getByText("gateway-local")).toBeInTheDocument());
  expect(screen.getByText("worker-local")).toBeInTheDocument();
  expect(screen.getByText("error")).toHaveClass("status", "error");
  expect(document.body.textContent).not.toContain("request input");
});
