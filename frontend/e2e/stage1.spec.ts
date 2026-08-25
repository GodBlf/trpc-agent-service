import { expect, test, type Page } from "@playwright/test";

async function expectNoHorizontalOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}

async function createActiveTenantApp(page: Page, suffix: string, ordinal: string) {
  const tenantId = `tenant-${suffix}-${ordinal}`;
  const appId = `app-${suffix}-${ordinal}`;
  const deploymentId = `deploy-${suffix}-${ordinal}`;

  await page.getByRole("button", { name: "租户" }).click();
  await page.getByRole("button", { name: "新建租户" }).click();
  await page.getByLabel("租户标识").fill(tenantId);
  await page.getByLabel("显示名称").fill(`验收租户 ${suffix} ${ordinal}`);
  await page.getByRole("button", { name: "创建租户" }).click();
  await expect(page.getByRole("heading", { name: `验收租户 ${suffix} ${ordinal}` })).toBeVisible();
  await page.getByRole("combobox", { name: "当前租户" }).selectOption(tenantId);

  await page.getByRole("button", { name: "Agent 应用" }).click();
  await page.getByRole("button", { name: "新建应用" }).click();
  await page.getByLabel("应用标识").fill(appId);
  await page.getByLabel("显示名称").fill(`验收应用 ${suffix} ${ordinal}`);
  await page.getByRole("button", { name: "创建应用" }).click();
  await expect(page.getByText(`验收应用 ${suffix} ${ordinal}`)).toBeVisible();

  await page.getByRole("button", { name: "部署" }).click();
  await page.getByRole("button", { name: "新建部署" }).click();
  await page.getByLabel("部署标识").fill(deploymentId);
  await page.getByRole("button", { name: "创建部署" }).click();
  await page.getByRole("button", { name: "创建版本" }).first().click();
  await page.getByLabel("JSON 配置").fill(`{"runner":"fake","scope":"${tenantId}"}`);
  await page.locator("form").getByRole("button", { name: "创建版本" }).click();
  await expect(page.getByText("v1")).toBeVisible();
  await page.getByRole("button", { name: "发布", exact: true }).click();
  await expect(page.getByText("published").first()).toBeVisible();
  await page.getByRole("button", { name: "激活" }).click();
  await expect(page.getByText("active").first()).toBeVisible();
  return { tenantId, appId, deploymentId };
}

async function runApp(page: Page, appId: string, sessionId: string, input: string) {
  return page.evaluate(async (request) => {
    const response = await fetch("/api/v1/admin/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(request),
    });
    return { status: response.status, body: await response.json() as { output?: string; error?: { code: string } } };
  }, { app_id: appId, session_id: sessionId, input });
}

test("complete Stage 1 management workflow", async ({ page }, testInfo) => {
  const suffix = testInfo.project.name === "desktop" ? "desk" : "mobile";
  await page.goto("/");
  await expect(page.getByText("Local Developer")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();

  const first = await createActiveTenantApp(page, suffix, "one");
  const firstRun = await runApp(page, first.appId, `session-${suffix}-one`, "hello-one");
  expect(firstRun).toEqual({ status: 200, body: { session_id: `session-${suffix}-one`, output: "echo:hello-one" } });

  const second = await createActiveTenantApp(page, suffix, "two");
  const secondRun = await runApp(page, second.appId, `session-${suffix}-two`, "hello-two");
  expect(secondRun).toEqual({ status: 200, body: { session_id: `session-${suffix}-two`, output: "echo:hello-two" } });
  const crossTenant = await runApp(page, first.appId, `session-${suffix}-cross`, "guess");
  expect(crossTenant.status).toBe(404);
  expect(crossTenant.body.error?.code).toBe("active_deployment_not_found");

  await page.getByRole("button", { name: "暂停" }).click();
  await expect(page.getByText("paused").first()).toBeVisible();

  await page.getByRole("button", { name: "运行节点" }).click();
  await expect(page.getByText("gateway-local")).toBeVisible();
  await expect(page.getByText("worker-local")).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await page.screenshot({ path: testInfo.outputPath("runtime-status.png"), fullPage: true });

  await page.getByRole("combobox", { name: "当前租户" }).selectOption("tenant-view");
  await page.getByRole("button", { name: "租户" }).click();
  await expect(page.getByRole("button", { name: "新建租户" })).toHaveCount(0);
  await page.getByRole("button", { name: "Agent 应用" }).click();
  await expect(page.getByRole("button", { name: "新建应用" })).toHaveCount(0);
  await page.getByRole("button", { name: "运行节点" }).click();
  await expect(page.getByText("没有访问权限")).toBeVisible();
});
