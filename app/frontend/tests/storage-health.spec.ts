import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const card = (page: Page) => page.getByRole("region", { name: "Storage and health", exact: true });

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  page.on("pageerror", (error) => { throw error; });
});

test("Overview shows both disks and CARE's health when everything is fine", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  await expect(card(page).getByLabel("Clinic data (engine disk)", { exact: true })).toContainText("48 GB free of 64 GB");
  await expect(card(page).getByLabel("Backup folder", { exact: true })).toContainText("418 GB free of 500 GB");
  await expect(card(page).getByLabel("Backup folder", { exact: true })).toContainText("/Library/Application Support/CARE Clinic/backups");
  await expect(card(page).getByRole("meter", { name: "Clinic data (engine disk): 25% used" })).toBeVisible();
  await expect(card(page).getByLabel("CARE health", { exact: true })).toContainText("Healthy");
  await expect(card(page)).toContainText("CARE server");
  await expect(card(page).getByRole("alert")).toHaveCount(0);
});

test("a nearly full disk and an unhealthy CARE are called out", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-disk-low");
  await expect(card(page).getByRole("alert")).toContainText("is almost full (3.0 GB free)");
  await expect(card(page).getByLabel("Clinic data (engine disk)", { exact: true })).toContainText("Almost full");
  await expect(card(page).getByLabel("Backup folder", { exact: true })).toContainText("Getting full");
  await expect(card(page).getByLabel("CARE health", { exact: true })).toContainText("Not answering");
  await expect(card(page).getByLabel("CARE health", { exact: true })).toContainText("HTTP 502");
  await expect(card(page).getByLabel("CARE health", { exact: true })).toContainText("restarting");
});

test("Check again reloads storage and health, and failures are shown", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  await expect(card(page).getByLabel("CARE health", { exact: true })).toContainText("Healthy");
  const count = () => page.evaluate(() => window.careTest.calls.filter((call) => call.method === "StorageStatus").length);
  const before = await count();
  await page.evaluate(() => window.careTest.failNext("StorageStatus", "the service didn't answer"));
  await card(page).getByRole("button", { name: "Check again", exact: true }).click();
  await expect(card(page).getByRole("alert")).toContainText("the service didn't answer");
  expect(await count()).toBeGreaterThan(before);
  await card(page).getByRole("button", { name: "Check again", exact: true }).click();
  await expect(card(page).getByRole("alert")).toHaveCount(0);
});
