import { expect, test } from "@playwright/test";

test("govern governance decisions and inspect their trace without secret disclosure", async ({ page }, testInfo) => {
  const suffix = `${testInfo.project.name}-${Date.now()}`;
  const appID = `stage5-app-${suffix}`;
  const deploymentID = `stage5-deploy-${suffix}`;
  const sessionID = `stage5-session-${suffix}`;
  const requestID = `stage5-request-${suffix}`;
  const secretCanary = `stage5-secret-${suffix}`;
  await page.goto("/");
  const pending = await page.evaluate(async ({ appID, deploymentID, sessionID, requestID, secretCanary, suffix }) => {
    const request = async (path: string, body: unknown, headers?: Record<string, string>) => {
      const response = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(body) });
      const payload = await response.json();
      if (!response.ok && response.status !== 409) throw new Error(`${path}: ${response.status}`);
      return payload as Record<string, unknown>;
    };
    await request("/api/v1/admin/agent-apps", { id: appID, name: `Stage 5 ${suffix}` });
    await request("/api/v1/admin/deployments", { id: deploymentID, agent_app_id: appID });
    const version = await request(`/api/v1/admin/deployments/${deploymentID}/versions`, { config: { runner: "framework", tools: ["deploy"] } }, { "Idempotency-Key": `stage5-${suffix}` });
    await request(`/api/v1/admin/deployments/${deploymentID}/transition`, { status: "published", version_id: version.id });
    await request(`/api/v1/admin/deployments/${deploymentID}/transition`, { status: "active" });
    await request("/api/v1/admin/governance/policy", { agent_app_id: appID, allowed_tools: ["deploy"], dangerous_tools: ["deploy"], redacted_patterns: [secretCanary], token_budget: 100, estimated_tokens_per_run: 5, rate_limit: 10, rate_window_seconds: 60 });
    await request("/api/v1/chat/sessions", { app_id: appID, session_id: sessionID });
    return request(`/api/v1/chat/sessions/${sessionID}/messages`, { input: `release ${secretCanary}` }, { "X-Request-ID": requestID });
  }, { appID, deploymentID, sessionID, requestID, secretCanary, suffix });

  expect(String(pending.confirmation_id)).not.toBe("");
  expect(String(pending.trace_id)).not.toBe("");
  await page.getByRole("button", { name: "治理观测" }).click();
  await page.getByRole("button", { name: "确认" }).click();
  const confirmationRow = page.getByText(requestID).locator("xpath=ancestor::tr");
  await expect(confirmationRow).toBeVisible();
  await confirmationRow.getByTitle("批准").click();
  await expect(confirmationRow.getByText("approved", { exact: true })).toBeVisible();

  await page.evaluate(async ({ sessionID, requestID, secretCanary }) => {
    const response = await fetch(`/api/v1/chat/sessions/${sessionID}/messages`, { method: "POST", headers: { "Content-Type": "application/json", "X-Request-ID": requestID }, body: JSON.stringify({ input: `release ${secretCanary}` }) });
    if (!response.ok) throw new Error(`retry: ${response.status}`);
  }, { sessionID, requestID, secretCanary });
  await page.waitForTimeout(100);
  await page.getByRole("button", { name: "指标与成本" }).click();
  await page.getByLabel("Request 或 Trace ID").fill(String(pending.trace_id));
  await page.getByRole("button", { name: "查询 Trace" }).click();
  await expect(page.getByText("gateway.receive").first()).toBeVisible();
  await expect(page.getByText("policy.evaluate").first()).toBeVisible();
  expect(await page.locator("body").innerText()).not.toContain(secretCanary);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
});
