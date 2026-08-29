import { render, screen, waitFor } from "@testing-library/react";
import App from "./App";

test("loads the server identity and renders stable console navigation", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({
      id: "developer", name: "Local Developer", active_tenant_id: "tenant-a", active_role: "platform_admin",
      assignments: [{ tenant_id: "tenant-a", tenant_name: "Tenant A", role: "platform_admin" }],
    }),
  }));
  render(<App />);
  expect(screen.getByText("正在加载")).toBeInTheDocument();
  await waitFor(() => expect(screen.getByText("Local Developer")).toBeInTheDocument());
  expect(screen.getByRole("navigation", { name: "主导航" })).toBeInTheDocument();
  expect(screen.getByRole("combobox", { name: "当前租户" })).toHaveValue("tenant-a");
});
