import { expect, test } from "@playwright/test";
import type {} from "./fixtures/host";

test.beforeEach(async ({ page, baseURL }) => {
  const origin = new URL(baseURL ?? "http://127.0.0.1:41783").origin.replace(/^http/, "ws");
  await page.routeWebSocket(`${origin}/**`, (socket) => socket.send('{"type":"connected"}'));
  await page.goto("/tests/fixtures/index.html?scenario=available");
});

test("in-place handoff never claims installation before exit or unlocks navigation", async ({ page }) => {
  await expect(page.getByText(/Updates this installed copy and reopens CARE Clinic/)).toBeVisible();
  await page.getByRole("button", { name: "Update now", exact: true }).click();
  for (const phase of ["verifying", "installing", "restarting"] as const) {
    await page.evaluate((next) => window.careTest.progress({ phase: next, done: 0, total: 0 }), phase);
    await expect(page.getByRole("button", { name: "Start setup", exact: true })).toBeDisabled();
  }
  await page.evaluate(() => window.careTest.finishUpdate());
  await expect(page.getByText("Restarting CARE Clinic to finish updating…", { exact: true })).toBeVisible();
  await expect(page.getByText(/Installed|Drag CARE Clinic|Follow it to finish/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Start setup", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: /^(OK|Done|Dismiss)$/ })).toHaveCount(0);
});

test("disk-image and development copies get actionable installation guidance", async ({ page }) => {
  await page.getByRole("button", { name: "Update now", exact: true }).click();
  await page.evaluate(() => window.careTest.finishUpdate(
    "update location unavailable: internal private installation path",
  ));
  await expect(page.getByRole("alert")).toContainText("Open an installed copy to update");
  await expect(page.getByRole("alert")).toContainText("outside the disk image");
  await expect(page.getByText("internal private installation path")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Start setup", exact: true })).toBeEnabled();
});

const methodCalls = (page: import("@playwright/test").Page, name: string) =>
  page.evaluate((method) => window.careTest.calls.filter((call) => call.method === method).length, name);

test("update available, then download progress, then restart to update", async ({ page }) => {
  await page.getByRole("button", { name: "Update now", exact: true }).click();
  await expect.poll(() => methodCalls(page, "DownloadAppUpdate")).toBe(1);
  await expect(page.getByText("Downloading CARE Clinic 0.1.6…", { exact: true })).toBeVisible();
  await expect(page.getByText("31.0 MB of 50.0 MB")).toBeVisible();
  await expect(page.getByRole("button", { name: "Start setup", exact: true })).toBeDisabled();

  await page.evaluate(() => window.careTest.downloaded());
  await expect(page.getByText("Ready to update", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Start setup", exact: true })).toBeEnabled();
  expect(await methodCalls(page, "RestartToUpdate")).toBe(0);

  await page.getByRole("button", { name: "Restart to update", exact: true }).click();
  await expect.poll(() => methodCalls(page, "RestartToUpdate")).toBe(1);
  await expect(page.getByText("Restarting CARE Clinic to finish updating…", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Start setup", exact: true })).toBeDisabled();
  expect(await methodCalls(page, "DownloadAppUpdate")).toBe(1);
});

test("a failed download keeps the current version and can be retried", async ({ page }) => {
  await page.getByRole("button", { name: "Update now", exact: true }).click();
  await page.evaluate(() => window.careTest.finishUpdate(
    "the CARE Clinic 0.1.6 update didn't download properly and nothing was installed (checksum mismatch)"));
  await expect(page.getByRole("alert")).toContainText("The download didn't come through properly");
  await expect(page.getByRole("button", { name: "Restart to update", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await expect.poll(() => methodCalls(page, "DownloadAppUpdate")).toBe(2);
});

test.describe("control panel", () => {
  const desktop = (page: import("@playwright/test").Page) =>
    page.getByRole("region", { name: "CARE Clinic application", exact: true });
  const openUpdates = async (page: import("@playwright/test").Page, scenario: string) => {
    await page.goto(`/tests/fixtures/index.html?scenario=${scenario}&screen=panel`);
    await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Updates/ }).click();
  };

  test("shows the unsigned status and offers restart once downloaded", async ({ page }) => {
    await openUpdates(page, "unsigned");
    await expect(desktop(page)).toContainText("0.1.6 available");
    await expect(desktop(page)).toContainText("Unsigned");
    await expect(desktop(page)).toContainText("This version is not signed");
    await desktop(page).getByRole("button", { name: "Update CARE Clinic", exact: true }).click();
    await expect(desktop(page).getByText("Downloading CARE Clinic 0.1.6…")).toBeVisible();
    await page.evaluate(() => window.careTest.downloaded());
    await expect(desktop(page)).toContainText("CARE Clinic 0.1.6 is ready to install");
    await desktop(page).getByRole("button", { name: "Restart to update", exact: true }).click();
    await expect(desktop(page).getByText("Installed — restarting CARE Clinic…")).toBeVisible();
  });

  test("a signed update shows no unsigned badge", async ({ page }) => {
    await openUpdates(page, "available");
    await expect(desktop(page)).toContainText("0.1.6 available");
    await expect(desktop(page)).not.toContainText("Unsigned");
  });

  test("the relaunched app reports finishing, then success", async ({ page }) => {
    await openUpdates(page, "panel");
    await page.evaluate(() => window.careTest.setOutcome({ state: "finishing", version: "0.1.6" }));
    await expect(desktop(page)).toContainText("Finishing the update to CARE Clinic 0.1.6…");
    await page.evaluate(() => {
      window.careTest.setOutcome({ state: "done", version: "0.1.6" });
      window.careTest.emit("care-done", 0, "app-update");
    });
    await expect(desktop(page)).toContainText("CARE Clinic 0.1.6 is installed");
    await desktop(page).getByRole("button", { name: "Dismiss", exact: true }).click();
    await expect(desktop(page)).not.toContainText("is installed");
  });

  test("an unfinished update explains itself and retries the remaining steps", async ({ page }) => {
    await openUpdates(page, "panel");
    await page.evaluate(() => {
      window.careTest.setOutcome({ state: "finishing", version: "0.1.6" });
      window.careTest.setOutcome({ state: "failed", version: "0.1.6", error: "couldn't update the CARE Clinic service: User canceled.", can_retry: true });
      window.careTest.emit("care-error", "The CARE Clinic update didn't finish", "couldn't update the CARE Clinic service: User canceled.");
      window.careTest.emit("care-done", 1, "app-update");
    });
    await expect(desktop(page)).toContainText("The update to CARE Clinic 0.1.6 didn't finish");
    await expect(desktop(page)).toContainText("User canceled.");
    await expect(desktop(page)).not.toContainText("CARE Clinic couldn't finish updating");
    await desktop(page).getByRole("button", { name: "Try again", exact: true }).click();
    await expect.poll(() => methodCalls(page, "FinishAppUpdate")).toBe(1);
    await expect(desktop(page)).toContainText("CARE Clinic 0.1.6 is installed");
  });
});
