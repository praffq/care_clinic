import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";
import { completeBackups, continueServerChecks, saveRecoveryCodes } from "./helpers/setup";

const password = "ClinicTest123";
const reminder = (page: Page) => page.getByLabel("Recovery codes reminder", { exact: true });
const modal = (page: Page) => page.getByRole("alertdialog");
const saveButton = (page: Page) => modal(page).getByRole("button", { name: "Choose where to save", exact: true });

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  page.on("pageerror", (error) => { throw error; });
});

async function panel(page: Page, remaining: number) {
  await page.goto(`/tests/fixtures/index.html?scenario=panel-running&recoveryCodes=${remaining}`);
  await expect(page.getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
}

for (const remaining of [0, 1, 2, 3, 6]) {
  test(`recovery reminder threshold and persistence with ${remaining} codes`, async ({ page }) => {
    await panel(page, remaining);
    await expect.poll(() => page.evaluate(() => window.careTest.calls.some((call) => call.method === "GetAdminRecoveryCodeCount"))).toBe(true);
    if (remaining > 2) {
      await expect(reminder(page)).toHaveCount(0);
      return;
    }
    const title = remaining === 0 ? "No recovery codes left." : `Only ${remaining} recovery ${remaining === 1 ? "code" : "codes"} left.`;
    await expect(reminder(page)).toContainText(title);
    await expect(reminder(page)).toHaveCSS("color", remaining === 0 ? "rgb(153, 27, 27)" : "rgb(146, 64, 14)");
    await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: "Home access", exact: true }).click();
    await expect(reminder(page)).toContainText(title);
    await page.reload();
    await expect(reminder(page)).toContainText(title);
    await expect(reminder(page).getByRole("button", { name: /dismiss|later/i })).toHaveCount(0);
  });
}

test("banner requires a password and stays through cancellation and failed saves", async ({ page }) => {
  await panel(page, 2);
  await reminder(page).getByRole("button", { name: "Save new recovery codes" }).click();
  await expect(modal(page)).toContainText("replaces all old codes");
  await expect(saveButton(page)).toBeDisabled();
  await modal(page).getByLabel("CARE Clinic admin password", { exact: true }).fill("wrong");
  await saveButton(page).click();
  await expect(modal(page)).toContainText("The CARE Clinic admin password didn't match");
  await expect(modal(page).getByLabel("CARE Clinic admin password", { exact: true })).toHaveValue("");
  await expect(reminder(page)).toContainText("Only 2 recovery codes left.");

  await page.evaluate(() => window.careTest.respond("SaveAdminRecoveryCodes", ""));
  await modal(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await saveButton(page).click();
  await expect(modal(page)).toContainText("No new recovery codes were saved");
  await expect(reminder(page)).toBeVisible();
  await page.evaluate(() => window.careTest.failNext("SaveAdminRecoveryCodes", "could not activate the new recovery codes; keep the previous sheet and retry: private error"));
  await modal(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await saveButton(page).click();
  await expect(modal(page)).toContainText("The new codes weren't activated");
  await expect(reminder(page)).toBeVisible();
  await modal(page).getByRole("button", { name: "Cancel", exact: true }).click();
  await reminder(page).getByRole("button", { name: "Save new recovery codes" }).click();
  await expect(modal(page).getByLabel("CARE Clinic admin password", { exact: true })).toHaveValue("");
});

test("successful replacement clears the reminder only after the native save completes", async ({ page }) => {
  await panel(page, 0);
  await reminder(page).getByRole("button", { name: "Save new recovery codes" }).click();
  await modal(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await page.evaluate(() => window.careTest.hold("SaveAdminRecoveryCodes"));
  await saveButton(page).click();
  await expect(modal(page).getByRole("button", { name: "Cancel", exact: true })).toBeDisabled();
  await expect(reminder(page)).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(modal(page)).toBeVisible();
  await page.evaluate(() => window.careTest.release("SaveAdminRecoveryCodes"));
  await expect(modal(page)).toHaveCount(0);
  await expect(reminder(page)).toHaveCount(0);
  expect(await page.evaluate(() => window.careTest.fixtures.recoveryCodes.filter(Boolean).length)).toBe(6);
});

test("using a recovery code updates the banner and Advanced replacement clears it", async ({ page }) => {
  await panel(page, 3);
  await page.getByRole("button", { name: "Advanced", exact: true }).click();
  await page.getByRole("button", { name: "Forgot CARE Clinic password?", exact: true }).click();
  const code = await page.evaluate(() => window.careTest.fixtures.recoveryCodes[0]);
  await page.getByLabel("Unused recovery code", { exact: true }).fill(code);
  await page.getByLabel("New CARE Clinic admin password", { exact: true }).fill("ClinicChanged456");
  await page.getByLabel("Confirm new CARE Clinic admin password", { exact: true }).fill("ClinicChanged456");
  await page.getByRole("button", { name: "Reset CARE Clinic password", exact: true }).click();
  await expect(reminder(page)).toContainText("Only 2 recovery codes left.");
  await page.getByRole("button", { name: "New recovery codes", exact: true }).click();
  await saveButton(page).click();
  await expect(modal(page)).toHaveCount(0);
  await expect(reminder(page)).toHaveCount(0);
});

test("an app update blocks an open recovery dialog and clears its password", async ({ page }) => {
  await panel(page, 1);
  await reminder(page).getByRole("button", { name: "Save new recovery codes" }).click();
  await modal(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await page.evaluate(() => window.careTest.progress({ phase: "verifying", done: 1, total: 1 }));
  await expect(modal(page).getByLabel("CARE Clinic admin password", { exact: true })).toHaveValue("");
  await expect(saveButton(page)).toBeDisabled();
  await expect(reminder(page)).toBeVisible();
  expect(await page.evaluate(() => window.careTest.calls.filter((call) => call.method === "SaveAdminRecoveryCodes").length)).toBe(0);
});

test("the recovery banner and save dialog fit a small window", async ({ page }) => {
  await page.setViewportSize({ width: 720, height: 560 });
  await panel(page, 0);
  await expect(reminder(page)).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await reminder(page).getByRole("button", { name: "Save new recovery codes" }).click();
  await expect(modal(page)).toBeVisible();
  const bounds = await modal(page).boundingBox();
  expect(bounds).not.toBeNull();
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.y).toBeGreaterThanOrEqual(0);
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(720);
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(560);
});

const forward = (page: Page) => page.getByRole("button", { name: "Continue", exact: true });
const codesCard = (page: Page) => page.getByRole("region", { name: "Recovery codes", exact: true });
const setupCalls = (page: Page, method: string) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).length, method);

async function setupAdmin(page: Page, platform = "darwin") {
  await page.goto(`/tests/fixtures/index.html?scenario=current&role=server&platform=${platform}`);
  await continueServerChecks(page);
  await page.getByLabel("Clinic address", { exact: true }).fill("care");
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await completeBackups(page);
}
async function fillSetupPassword(page: Page, value = password) {
  await page.getByLabel("Password", { exact: true }).fill(value);
  await page.getByLabel("Confirm password", { exact: true }).fill(value);
}

test("setup asks for recovery codes after the password and blocks Continue until they are saved", async ({ page }) => {
  await setupAdmin(page);
  const choose = codesCard(page).getByRole("button", { name: "Choose where to save", exact: true });
  await expect(codesCard(page)).toContainText("Forgot your CARE Clinic password?");
  await expect(choose).toBeDisabled();
  await expect(codesCard(page)).toContainText("Set your password first.");
  await page.getByLabel("Password", { exact: true }).fill(password);
  await expect(codesCard(page)).toContainText("Confirm your password first.");
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await expect(choose).toBeEnabled();
  await expect(forward(page)).toBeDisabled();
  await expect(page.getByText("Choose a password and save your recovery codes to continue.")).toBeVisible();

  await page.evaluate(() => { window.careTest.fixtures.setupCodesTarget = ""; });
  await choose.click();
  await expect.poll(() => setupCalls(page, "SaveSetupRecoveryCodes")).toBe(1);
  await expect(choose).toBeEnabled();
  await expect(forward(page)).toBeDisabled();

  await page.evaluate(() => { window.careTest.fixtures.setupCodesTarget = "/Users/test/Documents/CARE-care-clinic-admin-codes.txt"; });
  await saveRecoveryCodes(page);
  await expect(codesCard(page)).toContainText("Saved to /Users/test/Documents/CARE-care-clinic-admin-codes.txt");
  await expect(codesCard(page).getByRole("button", { name: "Open folder" })).toHaveCount(0);
  await expect(forward(page)).toBeEnabled();

  await codesCard(page).getByRole("button", { name: "Open to print", exact: true }).click();
  await expect.poll(() => setupCalls(page, "OpenSetupRecoveryCodes")).toBe(1);
  const before = await page.evaluate(() => window.careTest.fixtures.recoveryCodes[0]);
  await codesCard(page).getByRole("button", { name: "Save a new set", exact: true }).click();
  await expect.poll(() => setupCalls(page, "SaveSetupRecoveryCodes")).toBe(3);
  expect(await page.evaluate(() => window.careTest.fixtures.recoveryCodes[0])).not.toBe(before);

  await fillSetupPassword(page, "ChangedClinic456");
  await expect(codesCard(page)).toContainText("Recovery codes saved");
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Review before installing");
  await expect(page.getByRole("region", { name: "Admin login" })).toContainText("Password set · six recovery codes saved");
});

test("setup recovery code problems explain what to do", async ({ page }) => {
  await setupAdmin(page);
  await fillSetupPassword(page);
  await page.evaluate(() => window.careTest.failNext("SaveSetupRecoveryCodes", "keep recovery files outside CARE's settings, installation, logs and backup folders (/private)"));
  await codesCard(page).getByRole("button", { name: "Choose where to save", exact: true }).click();
  await expect(page.getByText("That folder belongs to CARE.", { exact: false })).toBeVisible();
  await expect(page.getByText("/private", { exact: false })).toHaveCount(0);
  await expect(forward(page)).toBeDisabled();

  await saveRecoveryCodes(page);
  await expect(page.getByText("That folder belongs to CARE.", { exact: false })).toHaveCount(0);
  await page.evaluate(() => { window.careTest.fixtures.setupCodesProblem = "missing"; });
  await codesCard(page).getByRole("button", { name: "Open to print", exact: true }).click();
  await expect(page.getByText("The recovery codes file couldn't be opened.", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Check saved files again", exact: true }).click();
  await expect(page.getByText("The recovery codes file isn't available")).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await codesCard(page).getByRole("button", { name: "Choose where to save", exact: true }).click();
  await expect(page.getByText("The recovery codes file isn't available")).toHaveCount(0);
  await expect(forward(page)).toBeEnabled();
});

test("setup on Windows can open the recovery codes folder and warns about OneDrive", async ({ page }) => {
  await setupAdmin(page, "windows");
  await expect(page.getByText("Desktop may sync to OneDrive.", { exact: false })).toBeVisible();
  await fillSetupPassword(page);
  await saveRecoveryCodes(page);
  await codesCard(page).getByRole("button", { name: "Open folder", exact: true }).click();
  await expect.poll(() => setupCalls(page, "OpenSetupRecoveryFolder")).toBe(1);
});
