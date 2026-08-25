import { expect, test, type Page } from "@playwright/test";

async function expectNoHorizontalOverflow(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
}

test("complete Stage 1 management workflow", async ({ page }, testInfo) => {
  const suffix = testInfo.project.name === "desktop" ? "desk" : "mobile";
  await page.goto("/");
  await expect(page.getByText("Local Developer")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();

  await page.getByRole("button", { name: "租户" }).click();
  await page.getByRole("button", { name: "新建租户" }).click();
  await page.getByLabel("租户标识").fill(`tenant-${suffix}`);
  await page.getByLabel("显示名称").fill(`验收租户 ${suffix}`);
  await page.getByRole("button", { name: "创建租户" }).click();
  await expect(page.getByRole("heading", { name: `验收租户 ${suffix}` })).toBeVisible();
  await page.getByRole("combobox", { name: "当前租户" }).selectOption(`tenant-${suffix}`);

  await page.getByRole("button", { name: "Agent 应用" }).click();
  await page.getByRole("button", { name: "新建应用" }).click();
  await page.getByLabel("应用标识").fill(`app-${suffix}`);
  await page.getByLabel("显示名称").fill(`验收应用 ${suffix}`);
  await page.getByRole("button", { name: "创建应用" }).click();
  await expect(page.getByText(`验收应用 ${suffix}`)).toBeVisible();

  await page.getByRole("button", { name: "部署" }).click();
  await page.getByRole("button", { name: "新建部署" }).click();
  await page.getByLabel("部署标识").fill(`deploy-${suffix}`);
  await page.getByRole("button", { name: "创建部署" }).click();
  await page.getByRole("button", { name: "创建版本" }).first().click();
  await page.getByLabel("JSON 配置").fill('{"runner":"fake"}');
  await page.locator("form").getByRole("button", { name: "创建版本" }).click();
  await expect(page.getByText("v1")).toBeVisible();
  await page.getByRole("button", { name: "发布", exact: true }).click();
  await expect(page.getByText("published").first()).toBeVisible();
  await page.getByRole("button", { name: "激活" }).click();
  await expect(page.getByText("active").first()).toBeVisible();
  await page.getByRole("button", { name: "暂停" }).click();
  await expect(page.getByText("paused").first()).toBeVisible();

  await page.getByRole("button", { name: "运行节点" }).click();
  await expect(page.getByText("gateway-local")).toBeVisible();
  await expect(page.getByText("worker-local")).toBeVisible();
  await expectNoHorizontalOverflow(page);
  await page.screenshot({ path: testInfo.outputPath("runtime-status.png"), fullPage: true });
});
