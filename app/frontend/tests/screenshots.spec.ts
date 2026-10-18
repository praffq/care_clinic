import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

// Captures the S19 screens for review. Run with SCREENSHOTS=<dir>.
const dir = process.env.SCREENSHOTS;
const password = "ClinicTest123";

test.skip(!dir, "set SCREENSHOTS to a folder to capture screens");
test.use({ viewport: { width: 1100, height: 1500 }, trace: "off" });
test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
});

async function open(page: Page, scenario: string, tab: string) {
  await page.goto(`/tests/fixtures/index.html?scenario=${scenario}`);
  await page.waitForFunction(() => !!window.careTest);
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: new RegExp(`^${tab}`) }).click();
}

const shot = (page: Page, name: string) => page.screenshot({ path: `${dir}/${name}.png`, animations: "disabled" });
const card = (page: Page) => page.getByRole("region", { name: "Clinic software", exact: true });

test("updates: available with release notes", async ({ page }) => {
  await open(page, "panel-update-available", "Updates");
  await card(page).getByRole("button", { name: "Check clinic updates" }).click();
  await expect(card(page)).toContainText("Update available");
  await card(page).scrollIntoViewIfNeeded();
  await shot(page, "updates-available");
});

test("updates: applying with live steps", async ({ page }) => {
  await open(page, "panel-update-available", "Updates");
  await card(page).getByRole("button", { name: "Check clinic updates" }).click();
  await card(page).getByRole("button", { name: "Apply update", exact: true }).click();
  await page.getByRole("alertdialog").getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await shot(page, "updates-apply-confirm");
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await page.getByRole("alertdialog").getByRole("button", { name: "Apply update" }).click();
  await page.evaluate(() => window.careTest.journal({
    id: "op-3", component: "care", from: "2026.3.0", to: "2026.4.0", revision: 8, state: "running",
    started_at: "2026-03-11T02:30:00Z", updated_at: "2026-03-11T02:30:00Z", steps: [
      { name: "preflight", status: "done", attempts: 1 }, { name: "backup", status: "done", attempts: 1 },
      { name: "images", status: "done", attempts: 1 }, { name: "stop-app", status: "done", attempts: 1 },
      { name: "write-env", status: "done", attempts: 1 }, { name: "migrate", status: "running", attempts: 1 },
      { name: "start-stack", status: "pending", attempts: 0 }, { name: "health", status: "pending", attempts: 0 },
    ],
  }));
  await expect(card(page).getByRole("list", { name: "Update steps" })).toBeVisible();
  await shot(page, "updates-applying");
});

test("updates: blocked, needs a newer desktop", async ({ page }) => {
  await open(page, "panel-update-needs-desktop", "Updates");
  await card(page).getByRole("button", { name: "Check clinic updates" }).click();
  await expect(card(page)).toContainText("A newer CARE Clinic app is needed first");
  await card(page).scrollIntoViewIfNeeded();
  await shot(page, "updates-blocked");
});

test("updates: interrupted and failed rollback", async ({ page }) => {
  await open(page, "panel-update-interrupted", "Updates");
  await expect(card(page)).toContainText("An update was interrupted");
  await card(page).scrollIntoViewIfNeeded();
  await shot(page, "updates-interrupted");
  await open(page, "panel-update-rollback-failed", "Updates");
  await expect(card(page)).toContainText("Putting the previous version back failed");
  await card(page).scrollIntoViewIfNeeded();
  await shot(page, "updates-rollback-failed");
});

test("backups: list, alerts, progress and restore", async ({ page }) => {
  await open(page, "panel-running", "Backups");
  await expect(page.getByRole("list", { name: "Backups", exact: true })).toBeVisible();
  await shot(page, "backups");
  await open(page, "panel-backups-alert", "Backups");
  await expect(page.getByText("Backups aren't being copied out")).toBeVisible();
  await shot(page, "backups-alert");
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await page.getByRole("button", { name: "Back up now", exact: true }).click();
  await page.evaluate(() => window.careTest.emit("backup-progress",
    { job: "backup", state: "copying", bytes: 260 * 2 ** 20, total: 420 * 2 ** 20, message: "Copying to the backup folder" }));
  await expect(page.getByRole("progressbar", { name: "Backup progress" })).toBeVisible();
  await shot(page, "backups-progress");
  await open(page, "panel-running", "Backups");
  await page.getByRole("button", { name: /^Restore the backup from/ }).nth(1).click();
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await shot(page, "backups-restore");
});

test("plugins: live steps, rollback reason and badges", async ({ page }) => {
  await open(page, "panel-plugin-rolled-back", "Advanced");
  const advanced = page.locator(".care-advanced");
  await advanced.getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await advanced.getByRole("button", { name: "Unlock", exact: true }).click();
  const plugins = page.locator("section[aria-labelledby='advanced-plugins-title']");
  await expect(plugins).toContainText("The last plugin change was undone");
  await plugins.scrollIntoViewIfNeeded();
  await shot(page, "plugins-rolled-back");
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await plugins.getByRole("button", { name: "Settings for care_multihost_files_be" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Save and restart CARE" }).click();
  await page.evaluate(() => window.careTest.journal({
    id: "op-p", component: "plugins", from: "2026.3.0", to: "2026.3.0", revision: 8, state: "running",
    started_at: "2026-03-11T02:30:00Z", updated_at: "2026-03-11T02:30:00Z", steps: [
      { name: "preflight", status: "done", attempts: 1 }, { name: "backup", status: "done", attempts: 1 },
      { name: "build", status: "running", attempts: 1 }, { name: "stop-app", status: "pending", attempts: 0 },
      { name: "write-env", status: "pending", attempts: 0 }, { name: "migrate", status: "pending", attempts: 0 },
      { name: "start-stack", status: "pending", attempts: 0 }, { name: "health", status: "pending", attempts: 0 },
    ],
  }));
  await expect(plugins.getByRole("list", { name: "Plugin change steps" })).toBeVisible();
  await plugins.scrollIntoViewIfNeeded();
  await shot(page, "plugins-progress");
});

test("plugins: settings with a masked secret", async ({ page }) => {
  await open(page, "panel-running", "Advanced");
  const advanced = page.locator(".care-advanced");
  await advanced.getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await advanced.getByRole("button", { name: "Unlock", exact: true }).click();
  const plugins = page.locator("section[aria-labelledby='advanced-plugins-title']");
  await plugins.getByRole("button", { name: "Add Filly" }).click();
  await page.getByRole("alertdialog").getByLabel("MEDISPEAK_API_KEY", { exact: true }).fill("sk-secret");
  await page.getByRole("alertdialog").getByLabel("MEDISPEAK_BASE_URL", { exact: true }).fill("https://medispeak.example");
  await page.getByRole("alertdialog").getByRole("button", { name: "Add and restart CARE" }).click();
  await expect(plugins.getByRole("list", { name: "Enabled plugins" })).toContainText("Filly");
  await plugins.getByRole("button", { name: "Settings for care_filly" }).click();
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await shot(page, "plugins-settings");
});

test("overview: engine and system", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  const system = page.getByRole("region", { name: "Engine and system", exact: true });
  await expect(system).toContainText("Fallback");
  await system.scrollIntoViewIfNeeded();
  await shot(page, "overview-system");
});
