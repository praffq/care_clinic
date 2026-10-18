import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const password = "ClinicTest123";
const calls = (page: Page, method: Parameters<Window["careTest"]["failNext"]>[0]) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).map((call) => call.args), method);
const overview = (page: Page) => page.locator('.panel-page[aria-label="Overview"]');
const backupsCard = (page: Page) => overview(page).getByRole("region", { name: "Backups", exact: true });
const advanced = (page: Page) => page.locator(".care-advanced");
const backupSettings = (page: Page) => advanced(page).getByRole("region", { name: "Backup recovery", exact: true });
const section = (page: Page, name: string) => page.getByRole("navigation", { name: "Clinic sections" })
  .getByRole("button", { name: new RegExp(`^${name}`) });

test.use({ trace: "off" });
test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
});

async function openPanel(page: Page, scenario = "panel-running") {
  await page.goto(`/tests/fixtures/index.html?scenario=${scenario}`);
  await expect(overview(page).getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
}

async function unlockAdvanced(page: Page) {
  await section(page, "Advanced").click();
  await advanced(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await advanced(page).getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(backupSettings(page)).toBeVisible();
}

test("Overview shows where backups are copied out", async ({ page }) => {
  await openPanel(page);
  const card = backupsCard(page);
  await expect(card.getByLabel("Backup folder status")).toContainText("Copied out");
  await expect(card).toContainText("/Library/Application Support/CARE Clinic/backups");
  await expect(card.getByRole("alert")).toHaveCount(0);
  await expect(card).toContainText("Add an extra copy on a USB drive or network share in Advanced.");
});

test("Overview raises a critical alert when backups aren't copied out", async ({ page }) => {
  await openPanel(page, "panel-backups-critical");
  const card = backupsCard(page);
  await expect(card.getByLabel("Backup folder status")).toContainText("Not copied out");
  await expect(card.getByRole("alert")).toContainText("Backup not copied out since");
  const extra = card.getByLabel("Extra copy status");
  await expect(extra).toContainText("Not connected");
  await expect(extra.getByRole("status")).toContainText("isn't connected; last copied never");
  await extra.getByRole("button", { name: "Copy now" }).click();
  await expect(extra.getByRole("alert")).toContainText("The extra copy didn't finish");
  expect(await calls(page, "CopyBackupsNow")).toHaveLength(1);
});

test("Copy now copies to a connected extra copy", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => {
    const backups = window.careTest.fixtures.backups;
    backups.copy = { dir: "/Volumes/CARE USB/backups", connected: true, last_copied: backups.last_exported, copied_at: "2026-10-18T00:06:00Z" };
    window.careTest.emitService();
  });
  const extra = backupsCard(page).getByLabel("Extra copy status");
  await expect(extra).toContainText("Copied");
  await extra.getByRole("button", { name: "Copy now" }).click();
  await expect(page.getByText("Backup copied")).toBeVisible();
});

test("the storage card removes unused engine images", async ({ page }) => {
  await openPanel(page);
  const button = overview(page).getByRole("region", { name: "Storage and health" }).getByRole("button", { name: "Remove unused images" });
  await button.click();
  await expect.poll(() => calls(page, "PruneEngineImages")).toHaveLength(1);
  await expect(button).toBeEnabled();
});

test("Advanced saves the recovery key and sets up the extra copy", async ({ page }) => {
  await openPanel(page);
  await unlockAdvanced(page);
  const card = backupSettings(page);
  await card.getByRole("button", { name: "Save recovery key…" }).click();
  await expect(page.getByText("Recovery key saved to /Volumes/KEYS/CARE-care-backup-recovery-key.pem")).toBeVisible();
  expect(await calls(page, "ExportBackupRecoveryKey")).toEqual([[password]]);

  await card.getByRole("button", { name: "Choose folder…" }).click();
  await expect(page.getByText("Backups will also be copied to /Volumes/CARE USB/backups")).toBeVisible();
  await expect(card).toContainText("Also copied to /Volumes/CARE USB/backups");
  await card.getByRole("button", { name: "Stop extra copy" }).click();
  await expect(page.getByText("Backups are no longer copied a second time")).toBeVisible();
  await expect(card.getByRole("button", { name: "Choose folder…" })).toBeVisible();
  expect(await calls(page, "ClearBackupCopyDir")).toEqual([[password]]);
});

test("a failed recovery key save explains what to do", async ({ page }) => {
  await openPanel(page);
  await unlockAdvanced(page);
  await page.evaluate(() => window.careTest.failNext("ExportBackupRecoveryKey", "no backup recovery key yet"));
  await backupSettings(page).getByRole("button", { name: "Save recovery key…" }).click();
  await expect(backupSettings(page)).toContainText("The recovery key wasn't saved");
});

test("Advanced restores a backup after confirmation", async ({ page }) => {
  await openPanel(page);
  await unlockAdvanced(page);
  const card = backupSettings(page);
  await card.getByRole("button", { name: "Restore from backup…" }).click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("daily-20261018T000000Z");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
  expect(await calls(page, "RestoreBackup")).toHaveLength(0);

  await card.getByRole("button", { name: "Restore from backup…" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Restore", exact: true }).click();
  await expect.poll(() => calls(page, "RestoreBackup")).toEqual([
    ["/Volumes/CARE USB/backups/daily-20261018T000000Z", "", password],
  ]);
  expect(await calls(page, "ChooseBackupToRestore")).toEqual([[false], [false]]);
});

test("restoring a backup from another computer uses its recovery key", async ({ page }) => {
  await openPanel(page);
  await unlockAdvanced(page);
  const card = backupSettings(page);
  await card.getByRole("checkbox").click();
  await card.getByRole("button", { name: "Restore from backup…" }).click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("/Volumes/KEYS/old-clinic-key.pem");
  await dialog.getByRole("button", { name: "Restore", exact: true }).click();
  await expect.poll(() => calls(page, "RestoreBackup")).toEqual([
    ["/Volumes/CARE USB/backups/daily-20261018T000000Z", "/Volumes/KEYS/old-clinic-key.pem", password],
  ]);
  expect(await calls(page, "ChooseBackupToRestore")).toEqual([[true]]);
});
