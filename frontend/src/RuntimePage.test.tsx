import { render, screen, waitFor } from "@testing-library/react";
import { RuntimePage } from "./RuntimePage";

test("renders backend-reported gateway and worker state without request data", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [
    { id: "gateway-local", role: "gateway", available: true, lifecycle: "healthy", active_executions: 0, completed_executions: 2, failed_executions: 0 },
    { id: "worker-local", role: "worker", available: false, lifecycle: "unavailable", active_executions: 0, completed_executions: 2, failed_executions: 1 },
  ] }) }));
  render(<RuntimePage />);
  await waitFor(() => expect(screen.getByText("gateway-local")).toBeInTheDocument());
  expect(screen.getByText("worker-local")).toBeInTheDocument();
  expect(screen.getByText("unavailable")).toBeInTheDocument();
  expect(document.body.textContent).not.toContain("request input");
});
