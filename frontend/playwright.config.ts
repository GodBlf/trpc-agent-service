import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  reporter: "line",
  use: { baseURL: "http://127.0.0.1:18080", trace: "retain-on-failure" },
  webServer: {
    command: "cd .. && ./build.sh && ./bin/trpc-service -addr 127.0.0.1:18080",
    url: "http://127.0.0.1:18080/healthz",
    reuseExistingServer: false,
    timeout: 120_000,
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], browserName: "chromium" } },
    { name: "mobile", use: { ...devices["iPhone 13"], browserName: "chromium" } },
  ],
});
