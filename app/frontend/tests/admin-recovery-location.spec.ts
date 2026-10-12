import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const path = "/saved/recovery/CARE-clinic-admin-codes.txt";
const hint = "Last saved recovery-sheet location:";
const caveat = "You may have moved, printed, or deleted the file since saving it.";
const root = (page: Page) => page.locator(".care-advanced");
const count = (page: Page) => page.evaluate(() =>
  window.careTest.calls.filter((call) => call.method === "GetAdminRecoveryCodesPath").length);

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  page.on("pageerror", (error) => { throw error; });
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  await page.getByRole("button", { name: "Advanced", exact: true }).click();
  await expect(root(page).getByLabel("CARE Clinic admin password", { exact: true })).toBeVisible();
});

async function openRecovery(page: Page) {
  await root(page).getByRole("button", { name: "Forgot CARE Clinic password?", exact: true }).click();
  await expect(root(page).getByLabel("Unused recovery code", { exact: true })).toBeVisible();
}

for (const problem of ["", "missing", "unreadable", "mismatch"]) {
  test(`recovery location remains a hint with sheet status "${problem || "saved"}"`, async ({ page }) => {
    await page.evaluate(({ path, problem }) => {
      window.careTest.fixtures.recovery.codes_path = path;
      window.careTest.fixtures.recovery.codes_saved = !problem;
      window.careTest.fixtures.recovery.codes_problem = problem;
    }, { path, problem });
    await openRecovery(page);
    await expect(root(page)).toContainText(hint);
    await expect(root(page)).toContainText(path);
    await expect(root(page)).toContainText(caveat);
    await expect(root(page)).toContainText("A code-based reset cannot unlock the local backup key");
    await expect(root(page).getByRole("alert")).toHaveCount(0);
    await expect(root(page)).not.toContainText(/sheet is missing|sheet is unreadable|sheet doesn't match/i);
    expect(await page.evaluate(() => window.careTest.calls.filter((call) =>
      ["GetSetupRecoveryStatus", "OpenSetupRecoveryCodes", "OpenSetupRecoveryFolder"].includes(call.method)))).toEqual([]);
    const code = await page.evaluate(() => window.careTest.fixtures.recoveryCodes[0]);
    await expect(root(page)).not.toContainText(code);
  });
}

test("an empty saved path shows no location or setup warning", async ({ page }) => {
  await openRecovery(page);
  await expect.poll(() => count(page)).toBeGreaterThan(0);
  await expect(root(page)).not.toContainText(hint);
  await expect(root(page)).not.toContainText(caveat);
  await expect(root(page).getByRole("alert")).toHaveCount(0);
});

test("location lookup failure is logged and does not block password reset", async ({ page }) => {
  await page.evaluate(() => {
    window.go.main.App.GetAdminRecoveryCodesPath = async () => { throw new Error("location lookup unavailable"); };
  });
  await openRecovery(page);
  await expect(root(page)).toContainText("The last saved recovery-sheet location couldn't be checked.");
  await expect.poll(() => page.evaluate(() => window.careTest.logs.some((line) =>
    line.includes("recovery-sheet location: location lookup unavailable")))).toBe(true);
  const code = await page.evaluate(() => window.careTest.fixtures.recoveryCodes[0]);
  await root(page).getByLabel("Unused recovery code", { exact: true }).fill(code);
  await root(page).getByLabel("New CARE Clinic admin password", { exact: true }).fill("ClinicChanged456");
  await root(page).getByLabel("Confirm new CARE Clinic admin password", { exact: true }).fill("ClinicChanged456");
  const reset = root(page).getByRole("button", { name: "Reset CARE Clinic password", exact: true });
  await expect(reset).toBeEnabled();
  await reset.click();
  await expect(page.getByLabel("Unused recovery code", { exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => window.careTest.fixtures.recoveryCodes.filter(Boolean).length)).toBe(5);
});

test("late results cannot reappear after cancellation and reopening reads a fresh path", async ({ page }) => {
  await page.evaluate((path) => {
    window.careTest.fixtures.recovery.codes_path = path;
    window.careTest.hold("GetAdminRecoveryCodesPath");
  }, path);
  await openRecovery(page);
  await expect.poll(() => count(page)).toBeGreaterThan(0);
  await root(page).getByRole("button", { name: "Cancel", exact: true }).click();
  await page.evaluate(() => window.careTest.release("GetAdminRecoveryCodesPath"));
  await expect(root(page).getByRole("button", { name: "Forgot CARE Clinic password?", exact: true })).toBeVisible();
  await expect(root(page)).not.toContainText(hint);
  const before = await count(page);
  const nextPath = "/another/location/latest-sheet.txt";
  await page.evaluate((path) => { window.careTest.fixtures.recovery.codes_path = path; }, nextPath);
  await openRecovery(page);
  await expect(root(page)).toContainText(nextPath);
  await expect(root(page)).not.toContainText(path);
  expect(await count(page)).toBeGreaterThan(before);
});

test("ordinary password change neither requests nor displays recovery location", async ({ page }) => {
  await page.evaluate((path) => { window.careTest.fixtures.recovery.codes_path = path; }, path);
  await root(page).getByLabel("CARE Clinic admin password", { exact: true }).fill("ClinicTest123");
  await root(page).getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(root(page).getByRole("button", { name: "Change password", exact: true })).toBeVisible();
  const before = await count(page);
  await root(page).getByRole("button", { name: "Change password", exact: true }).click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog.getByLabel("New CARE Clinic admin password", { exact: true })).toBeVisible();
  await expect(dialog).not.toContainText(hint);
  await expect(dialog).not.toContainText(path);
  expect(await count(page)).toBe(before);
});
