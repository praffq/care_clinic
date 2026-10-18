import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const password = "ClinicTest123";
const card = (page: Page) => page.getByRole("region", { name: "Clinic software", exact: true });
const dialog = (page: Page) => page.getByRole("alertdialog");
const history = (page: Page) => card(page).getByRole("list", { name: "Update history" });
const calls = (page: Page, method: string) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).map((call) => call.args), method);

test.use({ trace: "off" });
test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  page.on("pageerror", (error) => { throw error; });
});

async function open(page: Page, scenario: string) {
  await page.goto(`/tests/fixtures/index.html?scenario=${scenario}`);
  await page.waitForFunction(() => !!window.careTest);
  await page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button", { name: /^Updates/ }).click();
  await expect(card(page).getByRole("list", { name: "Update history" })).toBeVisible();
}

async function confirmWith(page: Page, button: string, secret = password) {
  await dialog(page).getByLabel("CARE Clinic admin password", { exact: true }).fill(secret);
  await dialog(page).getByRole("button", { name: button, exact: true }).click();
}

test("shows installed versions and the history with each result and reason", async ({ page }) => {
  await open(page, "panel-running");
  const versions = card(page).getByRole("definition");
  await expect(versions.nth(0)).toContainText("2026.3.0");
  await expect(versions.nth(1)).toContainText("1.2.3");
  await expect(versions.nth(2)).toContainText("0.1.5");
  await expect(card(page)).toContainText("Not checked yet");
  await expect(card(page)).not.toContainText("Unsigned feed");
  const rows = history(page).getByRole("listitem");
  await expect(rows.nth(0)).toContainText("Engine 1.2.2 → 1.2.3");
  await expect(rows.nth(0)).toContainText("Applied");
  await expect(rows.nth(1)).toContainText("CARE 2026.2.1 → 2026.3.0");
  await expect(rows.nth(1)).toContainText("Rolled back");
  await expect(rows.nth(1)).toContainText("Checking CARE answers: CARE didn't answer its health check within 5 minutes");
});

test("Check now reports that everything is up to date", async ({ page }) => {
  await open(page, "panel-running");
  await card(page).getByRole("button", { name: "Check clinic updates", exact: true }).click();
  await expect(card(page)).toContainText("Up to date");
  await expect(card(page)).toContainText("CARE and the engine are up to date.");
  expect(await calls(page, "CheckClinicUpdates")).toHaveLength(1);
});

test("a blocked check explains that a newer desktop app is needed", async ({ page }) => {
  await open(page, "panel-update-needs-desktop");
  await card(page).getByRole("button", { name: "Check clinic updates", exact: true }).click();
  await expect(card(page)).toContainText("Can't update yet");
  await expect(card(page)).toContainText("A newer CARE Clinic app is needed first");
  await expect(card(page)).toContainText("CARE 2026.4.0 requires desktop 0.3.0 (installed 0.1.5)");
  await expect(card(page).getByRole("button", { name: "Apply update" })).toHaveCount(0);
});

test("an available update shows the plan, release notes and the unsigned feed badge", async ({ page }) => {
  await open(page, "panel-update-available");
  await expect(card(page)).toContainText("Unsigned feed");
  await card(page).getByRole("button", { name: "Check clinic updates", exact: true }).click();
  await expect(card(page)).toContainText("Update available");
  const plan = card(page).getByRole("list", { name: "Planned update" });
  await expect(plan.getByRole("listitem").nth(0)).toContainText("Engine");
  await expect(plan.getByRole("listitem").nth(0)).toContainText("1.3.0");
  await expect(plan.getByRole("listitem").nth(1)).toContainText("2026.4.0");
  await expect(plan.getByRole("listitem").nth(1)).toContainText("Database changes");
  await expect(card(page)).toContainText("Downloaded and ready to install.");
  await plan.getByRole("button", { name: "CARE backend for CARE 2026.3.0 → 2026.4.0" }).click();
  expect(await calls(page, "OpenURL")).toContainEqual(["https://github.com/ohcnetwork/care/tree/v2026.4.0"]);
});

test("applying asks for the admin password and shows each step live", async ({ page }) => {
  await open(page, "panel-update-available");
  await card(page).getByRole("button", { name: "Check clinic updates", exact: true }).click();
  await card(page).getByRole("button", { name: "Apply update", exact: true }).click();
  await expect(dialog(page)).toContainText("Apply the clinic update?");
  await expect(dialog(page)).toContainText("CARE 2026.3.0 → 2026.4.0");

  await confirmWith(page, "Apply update", "wrong-password");
  await expect(dialog(page).getByRole("alert")).toContainText("That password didn't work.");
  await expect(dialog(page)).toBeVisible();

  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await confirmWith(page, "Apply update");
  await expect(dialog(page)).toBeHidden();
  await expect(card(page)).toContainText("Starting the update…");
  await page.evaluate(() => window.careTest.journal({
    id: "op-3", component: "care", from: "2026.3.0", to: "2026.4.0", revision: 8, state: "running",
    started_at: "2026-03-11T02:30:00Z", updated_at: "2026-03-11T02:30:00Z", steps: [
      { name: "preflight", status: "done", attempts: 1 },
      { name: "backup", status: "done", attempts: 1 },
      { name: "migrate", status: "running", attempts: 2 },
      { name: "health", status: "pending", attempts: 0 },
    ],
  }));
  const steps = card(page).getByRole("list", { name: "Update steps" });
  await expect(card(page)).toContainText("Updating CARE 2026.3.0 → 2026.4.0");
  await expect(steps.getByLabel("Backing up the clinic: done")).toBeVisible();
  await expect(steps.getByLabel("Updating the database: running")).toContainText("try 2");
  await expect(steps.getByLabel("Checking CARE answers: pending")).toBeVisible();
  await expect(card(page)).toContainText("Updating");
  await expect(card(page).getByRole("button", { name: "Check clinic updates" })).toBeDisabled();

  await page.evaluate(() => {
    window.careTest.fixtures.clinicUpdates.check = undefined;
    window.careTest.fixtures.clinicUpdates.history.unshift({ id: "op-3", component: "care", from: "2026.3.0", to: "2026.4.0",
      revision: 8, result: "applied", started_at: "2026-03-11T02:30:00Z", finished_at: "2026-03-11T02:40:00Z" });
    window.careTest.finishJob("updates");
  });
  await expect(history(page).getByRole("listitem").first()).toContainText("CARE 2026.3.0 → 2026.4.0");
  await expect(history(page).getByRole("listitem").first()).toContainText("Applied");
  await expect(card(page).getByRole("list", { name: "Update steps" })).toHaveCount(0);
});

test("the whole apply runs through to the history", async ({ page }) => {
  await open(page, "panel-update-available");
  await card(page).getByRole("button", { name: "Check clinic updates", exact: true }).click();
  await card(page).getByRole("button", { name: "Apply update", exact: true }).click();
  await confirmWith(page, "Apply update");
  await expect(history(page).getByRole("listitem").first()).toContainText("CARE 2026.3.0 → 2026.4.0");
  await expect(card(page).getByRole("definition").nth(0)).toContainText("2026.4.0");
  expect(await calls(page, "ApplyClinicUpdates")).toEqual([[password]]);
});

test("an interrupted update can be resumed", async ({ page }) => {
  await open(page, "panel-update-interrupted");
  await expect(card(page)).toContainText("Needs attention");
  await expect(card(page)).toContainText("An update was interrupted");
  const steps = card(page).getByRole("list", { name: "Interrupted update steps" });
  await expect(steps.getByLabel("Updating the database: running")).toBeVisible();
  await card(page).getByRole("button", { name: "Resume", exact: true }).click();
  await expect(dialog(page)).toContainText("Resume the interrupted update?");
  await confirmWith(page, "Resume update");
  await expect(card(page)).not.toContainText("An update was interrupted");
  await expect(history(page).getByRole("listitem").first()).toContainText("Applied");
  expect(await calls(page, "ResumeClinicUpdate")).toEqual([[password]]);
});

test("an interrupted update can be rolled back", async ({ page }) => {
  await open(page, "panel-update-interrupted");
  await card(page).getByRole("button", { name: "Roll back", exact: true }).click();
  await expect(dialog(page)).toContainText("Records added since then are lost.");
  await confirmWith(page, "Roll back");
  await expect(card(page)).not.toContainText("An update was interrupted");
  await expect(history(page).getByRole("listitem").first()).toContainText("Rolled back");
  expect(await calls(page, "RollBackClinicUpdate")).toEqual([[password]]);
});

test("a failed rollback shows the failing step and only offers to retry it", async ({ page }) => {
  await open(page, "panel-update-rollback-failed");
  await expect(card(page)).toContainText("Putting the previous version back failed");
  await expect(card(page)).toContainText("Updating the database: migration 0142 failed: column already exists");
  const rollback = card(page).getByRole("list", { name: "Rollback steps" });
  await expect(rollback.getByLabel("Restoring the database: failed")).toContainText("the backup couldn't be read: disk I/O error");
  await expect(rollback.getByLabel("Restoring the database: failed")).toContainText("try 2");
  await expect(card(page).getByRole("button", { name: "Resume", exact: true })).toHaveCount(0);
  await card(page).getByRole("button", { name: "Try the rollback again", exact: true }).click();
  await confirmWith(page, "Roll back");
  await expect(history(page).getByRole("listitem").first()).toContainText("Rolled back");
});

test("a failed apply shows the reason from the service", async ({ page }) => {
  await open(page, "panel-update-available");
  await card(page).getByRole("button", { name: "Check clinic updates", exact: true }).click();
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await card(page).getByRole("button", { name: "Apply update", exact: true }).click();
  await confirmWith(page, "Apply update");
  await expect(dialog(page)).toBeHidden();
  await page.evaluate(() => window.careTest.finishJob("updates",
    "the rest of this update needs CARE Clinic 0.3.0 first: update the app from the CARE Clinic application card, then apply again"));
  await expect(page.getByText("This update needs a newer CARE Clinic app (0.3.0).")).toBeVisible();
  await expect(card(page)).not.toContainText("Updating");
});
