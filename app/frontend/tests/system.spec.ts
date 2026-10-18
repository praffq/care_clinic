import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const card = (page: Page) => page.getByRole("region", { name: "Engine and system", exact: true });

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  page.on("pageerror", (error) => { throw error; });
});

test("Overview shows the app, service and engine versions and the engine slots", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  const versions = card(page).getByRole("definition");
  await expect(versions.nth(0)).toHaveText("0.1.5");
  await expect(versions.nth(1)).toHaveText("0.1.5");
  await expect(versions.nth(2)).toContainText("1.2.3");
  await expect(card(page)).toContainText("Engine running");
  await expect(card(page)).toContainText("3 d 4 h");
  const slots = card(page).getByRole("list", { name: "Engine versions installed" }).getByRole("listitem");
  await expect(slots.nth(0)).toContainText("1.2.3");
  await expect(slots.nth(0)).toContainText("In use");
  await expect(slots.nth(1)).toContainText("1.2.2");
  await expect(slots.nth(1)).toContainText("Fallback");
});

test("an engine without a fallback says so, and a stopped VM is shown", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  await page.waitForFunction(() => !!window.careTest);
  await page.evaluate(() => {
    const { system } = window.careTest.fixtures;
    system.fallback = undefined;
    system.slots = system.slots.filter((slot) => slot.current);
    system.engine = { ...system.engine!, state: "crashed", uptime_seconds: 0 };
  });
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Backups/ }).click();
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Overview/ }).click();
  await expect(card(page)).toContainText("Engine stopped unexpectedly");
  await expect(card(page)).toContainText("No fallback engine yet");
  await expect(card(page).getByRole("list", { name: "Engine versions installed" }).getByRole("listitem")).toHaveCount(1);
});
