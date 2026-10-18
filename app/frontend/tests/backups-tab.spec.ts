import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const password = "ClinicTest123";
const tab = (page: Page) => page.locator('.panel-page[aria-label="Backups"]');
const status = (page: Page) => page.getByRole("region", { name: "Backup status", exact: true });
const sets = (page: Page) => page.getByRole("list", { name: "Backups", exact: true });
const dialog = (page: Page) => page.getByRole("alertdialog");
const calls = (page: Page, method: string) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).map((call) => call.args), method);

test.use({ trace: "off" });
test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  page.on("pageerror", (error) => { throw error; });
});

async function open(page: Page, scenario = "panel-running") {
  await page.goto(`/tests/fixtures/index.html?scenario=${scenario}`);
  await page.waitForFunction(() => !!window.careTest);
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Backups/ }).click();
  await expect(sets(page)).toBeVisible();
}

test("Back up now runs through to a new verified set", async ({ page }) => {
  await open(page);
  await status(page).getByRole("button", { name: "Back up now", exact: true }).click();
  await expect(sets(page).getByRole("listitem")).toHaveCount(3);
  await expect(sets(page).getByRole("listitem").nth(0)).toContainText("Verified");
  expect(await calls(page, "BackUpNow")).toHaveLength(1);
});

test("lists the backup sets with their time, size and verified state", async ({ page }) => {
  await open(page);
  await expect(tab(page).getByRole("heading", { name: "Backups", exact: true })).toBeVisible();
  await expect(status(page)).toContainText("Copied out");
  await expect(status(page)).toContainText("Last copied out");
  await expect(status(page)).toContainText("/Library/Application Support/CARE Clinic/backups");
  const rows = sets(page).getByRole("listitem");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("daily-20261018T000000Z");
  await expect(rows.nth(0)).toContainText("412 MB");
  await expect(rows.nth(0)).toContainText("Verified");
  await expect(rows.nth(1)).toContainText("Copied");
  await expect(tab(page).getByRole("alert")).toHaveCount(0);
  await expect(tab(page).locator('[data-hook="extra-copy"]')).toContainText("Not set up");
  await expect(tab(page).locator('[data-hook="restore-new-computer"]')).toContainText("Restore from backup in Advanced");
});

test("backups that aren't copied out raise an alert and can't be restored", async ({ page }) => {
  await open(page, "panel-backups-alert");
  await expect(tab(page)).toContainText("Backups aren't being copied out");
  await expect(tab(page)).toContainText("Backups haven't been copied out since 17 Oct.");
  await expect(tab(page)).toContainText("The last copy to the backup folder failed");
  await expect(tab(page)).toContainText("no space left on device");
  await expect(status(page)).toContainText("Needs attention");
  await expect(status(page)).toContainText("oldest");
  const first = sets(page).getByRole("listitem").nth(0);
  await expect(first).toContainText("Not copied out");
  await expect(first.getByRole("button", { name: /^Restore the backup from/ })).toBeDisabled();
});

test("Back up now shows progress and adds the new set", async ({ page }) => {
  await open(page, "panel-backups-alert");
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await status(page).getByRole("button", { name: "Back up now", exact: true }).click();
  await expect(status(page)).toContainText("Backing up the clinic…");
  await page.evaluate(() => window.careTest.emit("backup-progress",
    { job: "backup", state: "copying", bytes: 210 * 2 ** 20, total: 420 * 2 ** 20, message: "Copying to the backup folder" }));
  await expect(status(page).getByRole("progressbar", { name: "Backup progress" })).toBeVisible();
  await expect(status(page)).toContainText("Copying to the backup folder · 210 MB of 420 MB");
  await expect(status(page).getByRole("button", { name: "Back up now", exact: true })).toBeDisabled();

  await page.evaluate(() => {
    const { fixtures } = window.careTest;
    const now = new Date().toISOString();
    fixtures.backupSets = [{ id: "daily-20261019T000000Z", created_at: now, size: 420 * 2 ** 20, dir: `${fixtures.backups.dir}/daily-20261019T000000Z`,
      copied: true, latest: true, verified_at: now }, ...fixtures.backupSets.map((set) => ({ ...set, latest: false }))];
    fixtures.backups = { ...fixtures.backups, verified_at: now, pending: 0, critical: false, message: undefined, last_error: undefined };
    window.careTest.finishJob("backup");
  });
  await expect(sets(page).getByRole("listitem")).toHaveCount(3);
  await expect(sets(page).getByRole("listitem").nth(0)).toContainText("Verified");
  await expect(tab(page)).not.toContainText("Backups aren't being copied out");
  await expect(status(page)).toContainText("Copied out");
});

test("restoring warns, needs the admin password, and shows progress", async ({ page }) => {
  await open(page);
  await sets(page).getByRole("listitem").nth(1).getByRole("button", { name: /^Restore the backup from/ }).click();
  await expect(dialog(page)).toContainText("Everything recorded after this backup will be lost");
  await expect(dialog(page).getByRole("button", { name: "Restore this backup" })).toBeDisabled();

  await dialog(page).getByLabel("CARE Clinic admin password", { exact: true }).fill("nope");
  await dialog(page).getByRole("button", { name: "Restore this backup" }).click();
  await expect(dialog(page).locator(".panel-inline-error")).toHaveText("That password didn't work. Enter the CARE Clinic admin password you chose during setup.");

  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await dialog(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await dialog(page).getByRole("button", { name: "Restore this backup" }).click();
  await expect(dialog(page)).toBeHidden();
  expect((await calls(page, "RestoreBackup")).slice(-1)[0]).toEqual(["/Library/Application Support/CARE Clinic/backups/daily-20261017T000000Z", "", password]);
  await expect(status(page)).toContainText("Restoring the backup…");
  await expect(status(page)).toContainText("CARE is unavailable until the restore finishes.");
  await expect(status(page).getByRole("button", { name: "Back up now", exact: true })).toBeDisabled();
  await page.evaluate(() => window.careTest.finishJob("restore"));
  await expect(status(page)).toContainText("Copied out");
});

test("the backup folder can be changed with the admin password", async ({ page }) => {
  await open(page);
  await status(page).getByRole("button", { name: "Change folder…", exact: true }).click();
  await expect(dialog(page)).toContainText("Change the backup folder?");
  await dialog(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await dialog(page).getByRole("button", { name: "Choose folder…", exact: true }).click();
  await expect(dialog(page)).toBeHidden();
  await expect(page.getByText("Backups now go to /Volumes/ClinicBackup/CARE")).toBeVisible();
  await expect(status(page)).toContainText("/Volumes/ClinicBackup/CARE");
});

test("cancelling the folder picker changes nothing", async ({ page }) => {
  await open(page);
  await page.evaluate(() => { window.careTest.fixtures.backupFolder = ""; });
  await status(page).getByRole("button", { name: "Change folder…", exact: true }).click();
  await dialog(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await dialog(page).getByRole("button", { name: "Choose folder…", exact: true }).click();
  await expect(dialog(page)).toBeHidden();
  await expect(status(page)).toContainText("/Library/Application Support/CARE Clinic/backups");
});

test("a failure to read the backups can be retried", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  await page.waitForFunction(() => !!window.careTest);
  await page.evaluate(() => window.careTest.failNext("ListBackups", "the service didn't answer"));
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Backups/ }).click();
  await expect(tab(page)).toContainText("Couldn't read the backups");
  await tab(page).getByRole("button", { name: "Try again", exact: true }).click();
  await expect(sets(page).getByRole("listitem")).toHaveCount(2);
});

test("the extra copy's state is shown, and Advanced manages it", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-backups-critical");
  await page.waitForFunction(() => !!window.careTest);
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Backups/ }).click();
  await expect(sets(page)).toBeVisible();
  const copy = tab(page).locator('[data-hook="extra-copy"]');
  await expect(copy).toContainText("/Volumes/CARE USB/backups");
  await expect(copy).toContainText("Not connected");
  await expect(tab(page)).toContainText("The extra copy needs attention");
  await copy.getByRole("button", { name: "Open Advanced", exact: true }).click();
  await expect(page.locator('.panel-page[aria-label="Backups"]')).toBeHidden();
});
