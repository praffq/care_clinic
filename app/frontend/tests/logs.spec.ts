import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

async function callCount(page: Page, method: string, action?: string) {
  return page.evaluate(({ method, action }) => window.careTest.calls.filter((call) =>
    call.method === method && (action === undefined || call.args[0] === action)).length, { method, action });
}

test("support log export requires the clinic-password warning", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-current");
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  const logs = page.locator('.panel-page[aria-label="Logs"]');
  await expect(logs.getByRole("heading", { name: "Logs", exact: true })).toBeVisible();
  await expect(logs.getByText("guest-system.log", { exact: true })).toBeVisible();
  await logs.getByRole("button", { name: "Open logs folder", exact: true }).click();
  await logs.getByRole("button", { name: "Download support logs", exact: true }).click();
  const warning = page.getByRole("alertdialog");
  await expect(warning.getByText("This file contains clinic passwords. Share it only with the CARE support team.", { exact: true })).toBeVisible();
  await expect(warning.getByText(/environment files, including secrets/)).toBeVisible();
  await warning.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  await expect.poll(() => callCount(page, "ExportDiagnostics")).toBe(0);
  await logs.getByRole("button", { name: "Download support logs", exact: true }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Download ZIP", exact: true }).click();
  await expect.poll(() => callCount(page, "ExportDiagnostics")).toBe(1);
});

test("logs page reports collection status", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-current");
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  const logs = page.locator('.panel-page[aria-label="Logs"]');
  await expect(logs.getByRole("status", { name: "Engine log collection" })).toContainText("Collecting");
  await expect(logs.getByText("CARE engine update required")).toHaveCount(0);
});

test("an outdated engine waits for explicit approval and is not applied on launch", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-engine-outdated");
  const overview = page.locator('.panel-page[aria-label="Overview"]');
  await expect(overview.getByText("CARE engine update required", { exact: true })).toBeVisible();
  await expect(overview.getByText(/waiting for your approval/)).toBeVisible();
  await expect.poll(() => callCount(page, "ClinicAction", "engine-update")).toBe(0);
  await expect.poll(() => callCount(page, "ClinicAction", "start")).toBe(0);
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  const logs = page.locator('.panel-page[aria-label="Logs"]');
  await expect(logs.getByRole("status", { name: "Engine log collection" })).toContainText("Engine update required");
  await expect(logs.getByText("CARE engine update required", { exact: true })).toBeVisible();
});

test("a failed engine update offers an explicit retry", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-engine-update-failed");
  const overview = page.locator('.panel-page[aria-label="Overview"]');
  await expect(overview.getByText("CARE engine update required", { exact: true })).toBeVisible();
  await expect(overview.getByText(/needs CARE engine 0\.1\.1, but 0\.1\.0 is running/)).toBeVisible();
  await page.getByRole("button", { name: "Logs", exact: true }).click();
  const logs = page.locator('.panel-page[aria-label="Logs"]');
  await expect(logs.getByRole("status", { name: "Engine log collection" })).toContainText("Engine update required");
  await logs.getByRole("button", { name: "Update CARE engine", exact: true }).click();
  await expect(logs.getByRole("button", { name: "Try the update again", exact: true })).toBeVisible();
  await expect(logs.getByText(/network is unreachable/)).toBeVisible();
  await logs.getByRole("button", { name: "Try the update again", exact: true }).click();
  await expect(logs.getByRole("status", { name: "Engine log collection" })).toContainText("Collecting");
  await expect(page.getByText("CARE engine update required")).toHaveCount(0);
});

test("an engine update that was rolled back isn't retried automatically on launch", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-engine-rolled-back");
  const status = page.getByRole("region", { name: "Clinic status" });
  await expect(status.getByRole("heading", { name: "Running" })).toBeVisible();
  const overview = page.locator('.panel-page[aria-label="Overview"]');
  await expect(overview.getByText("CARE engine update required", { exact: true })).toBeVisible();
  await expect(overview.getByText(/previous engine was put back: CARE engine: Docker did not start\. It isn.t tried again automatically/)).toBeVisible();
  await expect.poll(() => callCount(page, "ClinicAction", "engine-update")).toBe(0);
  await overview.getByRole("button", { name: "Try the update again", exact: true }).click();
  await expect(page.getByText("CARE engine update required")).toHaveCount(0);
});
