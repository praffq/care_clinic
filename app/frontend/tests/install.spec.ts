import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";
import { continueServerChecks, reachReview, saveRecoveryCodes } from "./helpers/setup";
import { applyProgress, currentStep, failActiveStep, SETUP_STEPS, stepsForRetry } from "../src/lib/run-steps";
import type { AppUpdate } from "../src/types";

const password = "ClinicTest123";
const clinicName = "care-hospital";
const availableUpdate: AppUpdate = {
  current: "0.1.5", version: "0.1.6", available: true,
  notes_url: "", asset: "test-only", size: 50e6,
};
type Step = { step: string; state: "active" | "done" | "skipped"; detail?: string; done?: number; total?: number };
// What the host sends for a fresh install that downloads CARE.
const hostSteps: Step[] = [
  { step: "service", state: "active" }, { step: "service", state: "done" },
  { step: "engine", state: "active" },
  { step: "engine", state: "active", detail: "Version 0.1.2", done: 0, total: 300e6 },
  { step: "engine", state: "active", detail: "Version 0.1.2", done: 300e6, total: 300e6 },
  { step: "engine", state: "done", detail: "Version 0.1.2" },
  { step: "engine-ready", state: "active" }, { step: "engine-ready", state: "done" },
  { step: "address", state: "active", detail: "care-hospital.local" }, { step: "address", state: "done", detail: "care-hospital.local" },
  { step: "images", state: "active" },
  { step: "images", state: "active", detail: "Downloading CARE server", done: 250e6, total: 1000e6 },
  { step: "images", state: "done" },
  { step: "configure", state: "active" }, { step: "configure", state: "done" },
  { step: "database", state: "active" }, { step: "database", state: "done" },
  { step: "start", state: "active" }, { step: "start", state: "done" },
  { step: "admin", state: "active" }, { step: "admin", state: "done" },
  { step: "computer", state: "active" },
];
const downloading: Step = { step: "images", state: "active", detail: "Downloading CARE server", done: 250e6, total: 1000e6 };
const send = (page: Page, ...steps: Step[]) => page.evaluate((list) => {
  for (const ev of list) window.careTest.emit("setup-progress", ev);
}, steps);
const forward = (page: Page) => page.getByRole("button", { name: "Continue", exact: true });
const install = (page: Page) => page.getByRole("button", { name: "Install", exact: true });
const retry = (page: Page) => page.getByRole("button", { name: "Try again", exact: true });
const failedHeading = (page: Page) => page.getByRole("heading", { name: "Something went wrong during installation", exact: true });
const calls = (page: Page, method: string) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).length, method);
const railLabels = (page: Page) => page.locator(".on-rail-step > span:nth-child(2)").allTextContents();

test.beforeEach(async ({ page }) => {
  // Shared-checkout edits must not replace a simulated run through Vite HMR.
  await page.routeWebSocket(/ws:\/\/127\.0\.0\.1:41783\//, (socket) => socket.send('{"type":"connected"}'));
  page.on("pageerror", (error) => { throw error; });
});

async function review(page: Page, { platform = "darwin" } = {}) {
  await page.goto(`/tests/fixtures/index.html?scenario=current&role=server&platform=${platform}`);
  await reachReview(page, { address: clinicName, password });
  await expect(install(page)).toBeEnabled();
}

async function begin(page: Page, options: { platform?: string } = {}) {
  await review(page, options);
  const labels = await railLabels(page);
  // RunSetup is a safe simulated acceptance; native installation never runs.
  await page.evaluate(() => window.careTest.respond("RunSetup", undefined));
  await install(page).click();
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  return labels;
}

async function fail(page: Page, offerUpdate = false) {
  if (offerUpdate) await page.evaluate((update) => window.careTest.setUpdate(update), availableUpdate);
  await page.evaluate(() => {
    window.careTest.emit("care-log", 'error: build failed: exit status 1 /private/diagnostics/fixture');
    window.careTest.emit("care-done", 1, "setup");
  });
  await expect(failedHeading(page)).toBeVisible();
  await expect(retry(page)).toBeEnabled();
  if (offerUpdate) await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeEnabled();
}

async function retryableFailure(page: Page, offerUpdate = false) {
  if (offerUpdate) await page.evaluate((update) => window.careTest.setUpdate(update), availableUpdate);
  await page.evaluate(() => {
    window.careTest.fixtures.setupStarted = true;
    window.careTest.emit("care-log", "error: download failed /private/diagnostics: exit status 1");
    window.careTest.emit("setup-failed", { can_retry: true });
    window.careTest.emit("care-done", 1, "setup");
  });
  await expect(failedHeading(page)).toBeVisible();
  await expect(retry(page)).toBeEnabled();
  if (offerUpdate) await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeEnabled();
}

const savedSetup = (page: Page) => page.evaluate(() => ({
  address: window.careTest.state.mdns_name,
  password: window.careTest.fixtures.adminPassword,
}));

async function fits(page: Page) {
  expect(await page.evaluate(() => {
    const footer = document.querySelector(".install-foot")!.getBoundingClientRect();
    return {
      pageX: document.documentElement.scrollWidth > innerWidth,
      pageY: document.documentElement.scrollHeight > innerHeight,
      innerX: [...document.querySelectorAll(".install-screen, .on-main, .on-body, .on-rail, .install-content")]
        .some((element) => element.scrollWidth > element.clientWidth + 1),
      footer: footer.left >= 0 && footer.right <= innerWidth + 1 && footer.bottom <= innerHeight + 1,
    };
  })).toEqual({ pageX: false, pageY: false, innerX: false, footer: true });
  await expect(page.getByRole("complementary", { name: "Setup progress" })).toHaveCSS("width", "272px");
}

async function capture(page: Page, name: string) {
  if (!process.env.CARE_SCREENSHOTS_DIR) return;
  await mkdir(process.env.CARE_SCREENSHOTS_DIR, { recursive: true });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(process.env.CARE_SCREENSHOTS_DIR, `${name}.png`), animations: "disabled" });
}

test("setup steps follow host progress events in order", () => {
  let steps = {};
  expect(currentStep(steps)).toBe(0);
  steps = applyProgress(steps, { step: "service", state: "active" });
  steps = applyProgress(steps, { step: "engine", state: "active" });
  expect(steps).toMatchObject({ service: { status: "done" }, engine: { status: "active" } });
  steps = applyProgress(steps, { step: "engine", state: "skipped", detail: "Version 0.1.1 is already installed" });
  steps = applyProgress(steps, { step: "nonsense", state: "active" });
  expect(Object.keys(steps)).toEqual(["service", "engine"]);
  expect(currentStep(steps)).toBe(2);
  steps = applyProgress(steps, { step: "images", state: "active", done: 5, total: 10 });
  steps = applyProgress(steps, { step: "images", state: "active", done: 7, total: 10 });
  expect(steps).toMatchObject({ images: { status: "active", done: 7, total: 10 } });
  steps = failActiveStep(steps);
  expect(steps).toMatchObject({ images: { status: "failed" } });
  expect(currentStep(steps)).toBe(SETUP_STEPS.findIndex((s) => s.id === "images"));
  expect(Object.keys(stepsForRetry(steps))).toEqual(["service", "engine"]);
});

test("installation lists every host step in order with download progress", async ({ page }) => {
  await begin(page);
  const updateChecks = await calls(page, "CheckAppUpdate");
  await expect(page.getByRole("heading", { name: SETUP_STEPS[0].label, exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeFocused();
  await expect(page.locator(".install-stage")).toHaveText(SETUP_STEPS.map((s) => new RegExp(s.label)));
  await expect(page.locator(".install-stage-pending")).toHaveCount(SETUP_STEPS.length);
  await expect(page.getByRole("button")).toHaveCount(1);
  for (const ev of hostSteps) {
    await send(page, ev);
    if (ev.state !== "active") continue;
    const label = SETUP_STEPS.find((s) => s.id === ev.step)!.label;
    await expect(page.getByRole("heading", { name: label, exact: true })).toBeVisible();
    await expect(page.locator(".install-stage[aria-current=step]")).toContainText(label);
    if (ev.total) {
      const pct = Math.floor((ev.done! / ev.total) * 100);
      await expect(page.getByRole("progressbar", { name: label })).toHaveAttribute("aria-valuenow", String(pct));
      await expect(page.locator(".install-stage-bytes")).toHaveText(`${(ev.done! / 1e6).toFixed(1)} MB of ${(ev.total / 1e6).toFixed(1)} MB · ${pct}%`);
      await capture(page, `install-progress-${ev.step}-${pct}`);
    }
  }
  await expect(page.locator(".install-stage-done")).toHaveCount(SETUP_STEPS.length - 1);
  await expect(page.getByText("Step 10 of 10", { exact: true })).toBeVisible();
  await expect(page.locator(".install-stage-done").filter({ hasText: SETUP_STEPS[1].label })).toContainText("Version 0.1.2");
  await page.evaluate(() => {
    window.careTest.emit("care-log", "error: Applying database migrations failed: private diagnosis");
    window.careTest.emit("care-done", 0, "prerequisite");
    window.careTest.emit("care-done", 1, "app-update");
  });
  await expect(page.locator(".install-screen")).not.toContainText(/private diagnosis|Technical details|Your clinic is ready/);
  await expect(page.getByRole("button", { name: /Back|Cancel|Update/ })).toHaveCount(0);
  expect(await calls(page, "CheckAppUpdate")).toBe(updateChecks);
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  await page.getByRole("button", { name: "Open log folder", exact: true }).click();
  expect(await calls(page, "OpenLogFolder")).toBe(1);
});

test("a reused engine shows as skipped with its version", async ({ page }) => {
  await begin(page);
  await send(page, { step: "service", state: "active" }, { step: "service", state: "done" },
    { step: "engine", state: "active" }, { step: "engine", state: "skipped", detail: "Version 0.1.1 is already installed" },
    { step: "engine-ready", state: "active" });
  await expect(page.locator(".install-stage-skipped")).toContainText("Version 0.1.1 is already installed");
  await expect(page.getByRole("heading", { name: SETUP_STEPS[2].label, exact: true })).toBeVisible();
  await capture(page, "install-engine-skipped");
});

for (const [platform, count] of [["windows", 7], ["darwin", 7]] as const) {
  test(`installation and failure keep the ${count}-step rail on ${platform} and fit both window sizes`, async ({ page }) => {
    const labels = await begin(page, { platform });
    expect(labels).toHaveLength(count);
    expect(await railLabels(page)).toEqual(labels);
    await expect(page.getByText(`Step ${count} of ${count}`, { exact: true })).toBeVisible();
    await send(page, ...hostSteps.slice(0, 10), downloading);
    for (const size of [{ width: 1100, height: 700 }, { width: 720, height: 560 }]) {
      await page.setViewportSize(size);
      await fits(page);
      await capture(page, `install-${platform}-${size.width}x${size.height}`);
    }
    await fail(page, true);
    await expect(page.getByText("It stopped at “Downloading CARE”.", { exact: false })).toBeVisible();
    expect(await railLabels(page)).toEqual(labels);
    await expect(page.locator(".on-rail-step[aria-current=step]")).toContainText("Installation stopped");
    await expect(page.locator(".install-version")).toHaveText("Version 0.1.5");
    await expect(page.getByRole("complementary").getByRole("button")).toHaveCount(0);
    for (const size of [{ width: 1100, height: 700 }, { width: 720, height: 560 }]) {
      await page.setViewportSize(size);
      await fits(page);
      await expect(retry(page)).toBeInViewport();
      await capture(page, `install-failed-${platform}-${size.width}x${size.height}`);
    }
  });
}

test("the warning measures 15 minutes without an actual care-log line, not total elapsed time", async ({ page }) => {
  await page.clock.install();
  await begin(page);
  await send(page, downloading);
  const warning = page.getByText("This is taking longer than usual", { exact: true });
  await page.clock.fastForward(14 * 60 * 1000 + 59 * 1000);
  await expect(warning).toHaveCount(0);
  await page.clock.fastForward(1000);
  await expect(warning).toBeVisible();
  await expect(page.getByText("Last reported step", { exact: true })).toBeVisible();
  await expect(page.locator(".install-screen")).not.toContainText(/Slow internet|still working|still progressing/);
  await capture(page, "install-no-log-warning");
  await page.evaluate(() => window.careTest.emit("care-log", "unmatched build output - still receiving host messages"));
  await page.clock.fastForward(1000);
  await expect(warning).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Downloading CARE", exact: true })).toBeVisible();
  await page.clock.fastForward(14 * 60 * 1000 + 58 * 1000);
  await expect(warning).toHaveCount(0);
  await page.evaluate(() => window.runtime.LogPrint("a UI-originated log message is not a care-log event"));
  await page.clock.fastForward(1000);
  await expect(warning).toBeVisible();
  await expect(page.getByRole("timer")).toContainText("30:");
});

for (const first of ["setup-done", "care-done"] as const) {
  test(`completion goes straight to the panel with ${first} arriving first`, async ({ page }) => {
    await begin(page);
    await page.evaluate((event) => {
      window.careTest.state.setup_done = true;
      if (event === "setup-done") window.careTest.emit("setup-done", true);
      else window.careTest.emit("care-done", 0, "setup");
    }, first);
    await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
    await expect(page.getByText("Your clinic is ready", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Open control panel", exact: true })).toHaveCount(0);
    await page.evaluate((event) => {
      if (event === "setup-done") window.careTest.emit("care-done", 0, "setup");
      else window.careTest.emit("setup-done", true);
    }, first);
    await expect(page.locator(".care-panel")).toBeVisible();
    await expect(page.locator(".install-screen")).toHaveCount(0);
    expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  });
}

for (const success of [true, false]) {
  test(`an early ${success ? "success" : "failure"} event is not overwritten by RunSetup acceptance`, async ({ page }) => {
    await review(page);
    await page.evaluate(() => {
      window.careTest.respond("RunSetup", undefined);
      window.careTest.hold("RunSetup");
    });
    await install(page).click();
    await expect.poll(() => calls(page, "RunSetup")).toBe(1);
    await page.evaluate((completed) => {
      if (completed) {
        window.careTest.state.setup_done = true;
        window.careTest.emit("setup-done", true);
      }
      window.careTest.emit("care-done", completed ? 0 : 1, "setup");
      window.careTest.release("RunSetup");
    }, success);
    if (success) await expect(page.locator(".care-panel")).toBeVisible();
    else await expect(failedHeading(page)).toBeVisible();
    await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toHaveCount(0);
  });
}

test("failure is honest about partial setup, keeps diagnostics in the log, and performs no cleanup on entry", async ({ page }) => {
  await begin(page);
  await fail(page);
  await expect(failedHeading(page)).toBeFocused();
  await expect(page.locator(".install-screen")).toContainText("Installation stopped. Your clinic isn't ready to use yet.");
  await expect(page.locator(".install-screen")).not.toContainText(/nothing is running|nothing was saved|throw.*away|exit status|\/private\/diagnostics|last output/i);
  await expect(page.locator(".install-screen pre")).toHaveCount(0);
  await expect(retry(page)).toHaveAttribute("aria-describedby", "install-retry-consequences");
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  await page.evaluate(() => window.careTest.failNext("OpenLogFolder", "open /private/diagnostics: exit status 1"));
  await page.getByRole("button", { name: "Open log folder", exact: true }).click();
  await expect(page.getByText("Couldn't open the log folder.", { exact: false })).toBeVisible();
  await expect(page.locator(".install-screen")).not.toContainText("/private/diagnostics");
});

for (const [error, friendly] of [
  ["permission denied at /private/install: exit status 1", "The unfinished installation couldn't be cleared."],
  ["User cancelled the authorization dialog (-128)", "Cleanup was cancelled."],
] as const) {
  test(`retry handles ${friendly.toLowerCase()} and resets the password`, async ({ page }) => {
    await begin(page);
    await fail(page, true);
    await page.evaluate((detail) => {
      window.careTest.hold("CleanupFailedInstall");
      window.careTest.failNext("CleanupFailedInstall", detail);
    }, error);
    await retry(page).evaluate((button: HTMLButtonElement) => { button.click(); button.click(); });
    await expect.poll(() => calls(page, "CleanupFailedInstall")).toBe(1);
    await expect(page.getByRole("button", { name: "Back to setup", exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeDisabled();
    await page.evaluate(() => window.careTest.release("CleanupFailedInstall"));
    await expect(page.getByText(friendly, { exact: false })).toBeVisible();
    await expect(failedHeading(page)).toBeVisible();
    await expect(page.locator(".install-screen")).not.toContainText(error);
    await expect(retry(page)).toBeEnabled();
    await retry(page).click();
    await continueServerChecks(page);
    await expect(page.getByRole("heading", { name: "Choosing the clinic address", exact: true })).toBeVisible();
    await expect(page.getByLabel("Clinic address", { exact: true })).toHaveValue(clinicName);
    await forward(page).click();
    await expect(page.locator("#setup-title")).toHaveText("Setting up backups");
    await forward(page).click();
    await expect(page.getByLabel("Password", { exact: true })).toHaveValue("");
    await expect(page.getByLabel("Confirm password", { exact: true })).toHaveValue("");
    expect(await calls(page, "RunSetup")).toBe(1);
  });
}

test("Back to setup returns quietly without cleanup or discarding saved choices", async ({ page }) => {
  await begin(page);
  await fail(page);
  await page.getByRole("button", { name: "Back to setup", exact: true }).click();
  await continueServerChecks(page);
  await expect(page.getByRole("heading", { name: "Choosing the clinic address", exact: true })).toBeVisible();
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  await expect(page.getByLabel("Clinic address", { exact: true })).toHaveValue(clinicName);
  await forward(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Setting up backups");
  await forward(page).click();
  await expect(page.getByLabel("Password", { exact: true })).toHaveValue(password);
});

test("repeated installation retries preserve the original setup until native success", async ({ page }) => {
  const labels = await begin(page);
  await retryableFailure(page);
  const saved = await savedSetup(page);
  for (let attempt = 1; attempt <= 2; attempt++) {
    await retry(page).click();
    await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
    await expect.poll(() => calls(page, "RetrySetup")).toBe(attempt);
    expect(await railLabels(page)).toEqual(labels);
    expect(await savedSetup(page)).toEqual(saved);
    expect(await calls(page, "RunSetup")).toBe(1);
    expect(await calls(page, "CleanupFailedInstall")).toBe(0);
    await expect(page.locator(".care-panel")).toHaveCount(0);
    if (attempt === 1) await retryableFailure(page);
  }
  expect(await page.evaluate(() => window.careTest.calls.filter((call) => call.method === "RetrySetup").map((call) => call.args))).toEqual([[], []]);
  await page.evaluate(() => {
    window.careTest.state.setup_done = true;
    window.careTest.emit("setup-done", true);
  });
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  await page.evaluate(() => window.careTest.emit("care-done", 0, "setup"));
  await expect(page.locator(".care-panel")).toBeVisible();
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
});

test("a rejected installation retry stays recoverable and cannot run twice", async ({ page }) => {
  await begin(page);
  await retryableFailure(page);
  const saved = await savedSetup(page);
  await page.evaluate(() => {
    window.careTest.hold("RetrySetup");
    window.careTest.failNext("RetrySetup", "missing clinic configuration /private/diagnostics");
  });
  await retry(page).evaluate((button: HTMLButtonElement) => { button.click(); button.click(); });
  await expect.poll(() => calls(page, "RetrySetup")).toBe(1);
  await expect(page.getByRole("button", { name: "Back to setup", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Retrying installation…", exact: true })).toBeDisabled();
  await expect(page.locator(".install-screen")).not.toContainText("Clearing the unfinished installation");
  await page.evaluate(() => window.careTest.release("RetrySetup"));
  await expect(page.getByText("Your saved setup has been kept.", { exact: false })).toBeVisible();
  await expect(page.locator(".install-screen")).not.toContainText("/private/diagnostics");
  await expect(retry(page)).toBeEnabled();
  expect(await savedSetup(page)).toEqual(saved);
  await retry(page).click();
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  expect(await calls(page, "RetrySetup")).toBe(2);
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
});

for (const first of ["setup-done", "care-done"] as const) {
  test(`an early retry success with ${first} first is not overwritten by acceptance`, async ({ page }) => {
    await begin(page);
    await retryableFailure(page);
    await page.evaluate(() => {
      window.careTest.respond("RetrySetup", undefined);
      window.careTest.hold("RetrySetup");
    });
    await retry(page).click();
    await expect.poll(() => calls(page, "RetrySetup")).toBe(1);
    await page.evaluate((event) => {
      window.careTest.state.setup_done = true;
      if (event === "setup-done") window.careTest.emit("setup-done", true);
      else window.careTest.emit("care-done", 0, "setup");
      window.careTest.release("RetrySetup");
    }, first);
    await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
    await expect(page.locator(".care-panel")).toHaveCount(0);
    await page.evaluate((event) => {
      if (event === "setup-done") window.careTest.emit("care-done", 0, "setup");
      else window.careTest.emit("setup-done", true);
    }, first);
    await expect(page.locator(".care-panel")).toBeVisible();
    expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  });
}

test("retry failure metadata survives acceptance before the completion event", async ({ page }) => {
  await begin(page);
  await retryableFailure(page);
  await page.evaluate(() => window.careTest.hold("RetrySetup"));
  await retry(page).click();
  await expect.poll(() => calls(page, "RetrySetup")).toBe(1);
  await page.evaluate(() => {
    window.careTest.emit("setup-failed", { can_retry: true });
    window.careTest.release("RetrySetup");
  });
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  await page.evaluate(() => window.careTest.emit("care-done", 1, "setup"));
  await expect(failedHeading(page)).toBeVisible();
  await expect(page.getByText("Your setup choices are kept", { exact: true })).toBeVisible();
  await retry(page).click();
  await expect.poll(() => calls(page, "RetrySetup")).toBe(2);
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
});

test("an early retry failure is not replaced by late acceptance or unrelated failure metadata", async ({ page }) => {
  await begin(page);
  await retryableFailure(page);
  await page.evaluate(() => {
    window.careTest.respond("RetrySetup", undefined);
    window.careTest.hold("RetrySetup");
  });
  await retry(page).click();
  await expect.poll(() => calls(page, "RetrySetup")).toBe(1);
  await page.evaluate(() => {
    window.careTest.emit("setup-failed", { can_retry: true });
    window.careTest.emit("care-done", 1, "setup");
    window.careTest.release("RetrySetup");
    window.careTest.emit("setup-failed", { can_retry: false });
  });
  await expect(failedHeading(page)).toBeVisible();
  await expect(retry(page)).toBeEnabled();
  await retry(page).click();
  await expect.poll(() => calls(page, "RetrySetup")).toBe(2);
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
});

for (const first of ["retry", "update"] as const) {
  test(`resuming setup, updating, and Back cannot race when ${first} is clicked first`, async ({ page }) => {
    await begin(page);
    await retryableFailure(page, true);
    await page.evaluate((action) => {
      window.careTest.hold("RetrySetup");
      const button = (label: string) => [...document.querySelectorAll("button")].find((item) => item.textContent?.trim() === label)!;
      const retry = button("Try again");
      const update = button("Update now");
      const back = button("Back to setup");
      if (action === "retry") { retry.click(); update.click(); }
      else { update.click(); retry.click(); }
      back.click();
    }, first);
    if (first === "retry") {
      await expect.poll(() => calls(page, "RetrySetup")).toBe(1);
      expect(await calls(page, "DownloadAppUpdate")).toBe(0);
      await page.evaluate(() => window.careTest.release("RetrySetup"));
      await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
    } else {
      await expect.poll(() => calls(page, "DownloadAppUpdate")).toBe(1);
      expect(await calls(page, "RetrySetup")).toBe(0);
      await expect(retry(page)).toBeDisabled();
      await page.evaluate(() => {
        window.careTest.emit("setup-failed", { can_retry: false });
        window.careTest.finishUpdate("preview update cancelled");
      });
      await expect(failedHeading(page)).toBeVisible();
      await expect(retry(page)).toBeEnabled();
    }
    expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  });
}

for (const failure of ["couldn't reach GitHub to check for updates: private transport error", "release 0.1.6 has no installer for this computer"]) {
  test(`an unavailable update check never becomes an offer: ${failure.slice(0, 25)}`, async ({ page }) => {
    await begin(page);
    await page.evaluate((error) => {
      window.careTest.hold("CheckAppUpdate");
      window.careTest.failNext("CheckAppUpdate", error);
    }, failure);
    await fail(page);
    await expect(page.getByText("Checking for a CARE Clinic update…", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Update now", exact: true })).toHaveCount(0);
    await expect(retry(page)).toBeEnabled();
    await page.evaluate(() => window.careTest.release("CheckAppUpdate"));
    await expect(page.getByRole("button", { name: "Check again", exact: true })).toBeEnabled();
    await expect(page.getByRole("button", { name: "Update now", exact: true })).toHaveCount(0);
    await expect(page.locator(".install-screen")).not.toContainText(/Up to date|private transport error|seems to be offline/);
    await page.evaluate((update) => window.careTest.setUpdate(update), availableUpdate);
    await page.getByRole("button", { name: "Check again", exact: true }).click();
    await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeEnabled();
    expect(await calls(page, "DownloadAppUpdate")).toBe(0);
  });
}

test("an update stays busy after job acceptance and failure offers a separate update retry", async ({ page }) => {
  await begin(page);
  await fail(page, true);
  await page.getByRole("button", { name: "Update now", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Downloading CARE Clinic 0.1.6…", exact: true })).toBeVisible();
  await expect(retry(page)).toBeDisabled();
  await expect(page.getByRole("button", { name: "Back to setup", exact: true })).toBeDisabled();
  await page.evaluate(() => window.careTest.finishUpdate("couldn't replace the application: /private/failure exit status 1"));
  await expect(page.getByRole("heading", { name: "The CARE Clinic update didn't finish", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Retry update", exact: true })).toBeEnabled();
  await expect(retry(page)).toBeEnabled();
  await expect(page.locator(".install-screen")).not.toContainText("/private/failure");
  await page.getByRole("button", { name: "Retry update", exact: true }).click();
  await expect.poll(() => calls(page, "DownloadAppUpdate")).toBe(2);
  expect(await calls(page, "CleanupFailedInstall")).toBe(0);
  for (const [phase, heading] of [
    ["verifying", "Checking the CARE Clinic download…"],
    ["installing", "Updating the CARE Clinic application…"],
    ["installer", "The CARE Clinic installer has opened"],
  ] as const) {
    await page.evaluate((next) => window.careTest.progress({ phase: next, done: 50e6, total: 50e6 }), phase);
    await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
    await expect(retry(page)).toBeDisabled();
  }
  await page.evaluate(() => window.careTest.finishUpdate());
  await expect(page.getByText("Finish updating in the installer, then reopen CARE Clinic before trying setup again.", { exact: true })).toBeVisible();
  await expect(retry(page)).toBeDisabled();
});

for (const first of ["retry", "update"] as const) {
  test(`cleanup, update, and Back cannot race when ${first} is clicked first`, async ({ page }) => {
    await begin(page);
    await fail(page, true);
    await page.evaluate((action) => {
      window.careTest.hold("CleanupFailedInstall");
      const button = (label: string) => [...document.querySelectorAll("button")].find((item) => item.textContent?.trim() === label)!;
      const retry = button("Try again");
      const update = button("Update now");
      const back = button("Back to setup");
      if (action === "retry") { retry.click(); update.click(); }
      else { update.click(); retry.click(); }
      back.click();
    }, first);
    if (first === "retry") {
      await expect.poll(() => calls(page, "CleanupFailedInstall")).toBe(1);
      expect(await calls(page, "DownloadAppUpdate")).toBe(0);
      await expect(failedHeading(page)).toBeVisible();
      await page.evaluate(() => window.careTest.release("CleanupFailedInstall"));
      await continueServerChecks(page);
      await expect(page.getByRole("heading", { name: "Choosing the clinic address", exact: true })).toBeVisible();
    } else {
      await expect.poll(() => calls(page, "DownloadAppUpdate")).toBe(1);
      expect(await calls(page, "CleanupFailedInstall")).toBe(0);
      await expect(failedHeading(page)).toBeVisible();
      await expect(retry(page)).toBeDisabled();
      await page.evaluate(() => window.careTest.finishUpdate("preview update cancelled"));
      await expect(retry(page)).toBeEnabled();
    }
  });
}

test("reduced motion keeps the current stage readable without animation", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 720, height: 560 });
  await begin(page);
  await send(page, { step: "images", state: "active" });
  await expect(page.locator(".install-progress [data-slot=progress-indicator]")).toHaveCSS("animation-name", "none");
  await expect(page.getByRole("heading", { name: "Downloading CARE", exact: true })).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Open log folder", exact: true })).toBeFocused();
  await expect(page.getByRole("button", { name: "Open log folder", exact: true })).toBeInViewport();
  await fits(page);
});

test("small installation text and destructive actions retain readable contrast", async ({ page }) => {
  await begin(page);
  const contrast = async (selectors: string[]) => page.evaluate((targets) => {
    const values = (color: string) => (color.match(/[\d.]+/g) || []).map(Number);
    const luminance = (color: number[]) => color.slice(0, 3).map((value) => {
      const channel = value / 255;
      return channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4;
    }).reduce((sum, channel, index) => sum + channel * [.2126, .7152, .0722][index], 0);
    return targets.map((selector) => {
      const element = document.querySelector(selector)!;
      const text = luminance(values(getComputedStyle(element).color));
      let background = values("rgb(255,255,255)");
      for (let parent: Element | null = element; parent; parent = parent.parentElement) {
        const color = values(getComputedStyle(parent).backgroundColor);
        if (color.length === 3 || color[3] === 1) { background = color; break; }
      }
      const surface = luminance(background);
      return { selector, ratio: (Math.max(text, surface) + .05) / (Math.min(text, surface) + .05) };
    });
  }, selectors);
  for (const result of await contrast([".on-subtitle", ".install-elapsed", ".install-support > p", ".install-support .on-log button", ".on-foot-note"])) {
    expect(result.ratio, result.selector).toBeGreaterThanOrEqual(4.5);
  }
  await fail(page, true);
  for (const result of await contrast([".install-update-primary", ".install-retry-action", ".install-failure-card p", ".install-update p"])) {
    expect(result.ratio, result.selector).toBeGreaterThanOrEqual(4.5);
  }
});

async function backupStep(page: Page, platform = "darwin") {
  await page.goto(`/tests/fixtures/index.html?scenario=current&role=server&platform=${platform}`);
  await continueServerChecks(page);
  await page.getByLabel("Clinic address", { exact: true }).fill(clinicName);
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Setting up backups");
}

test("the backup step blocks Continue until the recovery file is saved and checked", async ({ page }) => {
  await backupStep(page);
  await expect(page.getByText("CARE makes an encrypted backup automatically, once every 24 hours.", { exact: false })).toBeVisible();
  await expect(page.getByText("/Library/Application Support/CARE Clinic/backups", { exact: true })).toBeVisible();
  await expect(page.getByText("418 GB free · backups need at least 1.0 GB", { exact: true })).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await expect(page.getByRole("button", { name: "Select saved file", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "Change folder", exact: true }).click();
  await expect(page.getByText("/Volumes/ClinicBackup/CARE", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Choose where to save", exact: true }).click();
  await expect(page.getByText("/Volumes/KEYS/CARE-care-backup-recovery-key.pem", { exact: true })).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await page.evaluate(() => { window.careTest.fixtures.recoveryPickPath = "/Volumes/KEYS/old.pem"; });
  await page.getByRole("button", { name: "Select saved file", exact: true }).click();
  await expect(page.getByText("That file doesn't match this clinic.", { exact: false }).first()).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await page.evaluate(() => { window.careTest.fixtures.recoveryPickPath = ""; });
  await page.getByRole("button", { name: "Select saved file", exact: true }).click();
  await expect(page.getByText("Recovery file checked", { exact: true })).toBeVisible();
  await expect(page.getByText("Keep this file safe, and don't share it with anyone", { exact: true })).toBeVisible();
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Creating the admin password");
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await saveRecoveryCodes(page);
  await forward(page).click();
  await expect(page.getByRole("region", { name: "Backups" })).toContainText("/Volumes/ClinicBackup/CARE");
  await page.evaluate(() => window.careTest.respond("RunSetup", undefined));
  await install(page).click();
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.careTest.calls.find((call) => call.method === "RunSetup")?.args))
    .toEqual([`${clinicName}.local`, password, "/Volumes/ClinicBackup/CARE"]);
});

test("a backup folder problem blocks saving the recovery file", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=setup-backup-readonly&role=server");
  await continueServerChecks(page);
  await forward(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Setting up backups");
  await expect(page.getByRole("alert").filter({ hasText: "read-only" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Choose where to save", exact: true })).toBeDisabled();
  await expect(forward(page)).toBeDisabled();
  await page.evaluate(() => { window.careTest.fixtures.setupFolderProblem = ""; });
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("button", { name: "Choose where to save", exact: true })).toBeEnabled();
});

test("Review sends a lost recovery file back to the backup step", async ({ page }) => {
  await review(page);
  await expect(page.getByRole("region", { name: "Backups" })).toContainText("recovery file saved and checked");
  await page.evaluate(() => {
    window.careTest.fixtures.setupBackup.backup_problem = "missing";
    window.careTest.fixtures.setupBackup.backup_verified = false;
  });
  await install(page).click();
  await expect(page.getByRole("region", { name: "Backups" })).toContainText("Save and check the backup recovery file before installing.");
  expect(await calls(page, "RunSetup")).toBe(0);
  await page.getByRole("button", { name: "Fix Backups" }).click();
  await expect(page.locator("#setup-title")).toHaveText("Setting up backups");
  await expect(page.getByText("The recovery file isn't ready", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Save a new recovery file", exact: true })).toBeEnabled();
});

test("Windows offers to open the recovery file's folder and warns about OneDrive", async ({ page }) => {
  await backupStep(page, "windows");
  await page.getByRole("button", { name: "Choose where to save", exact: true }).click();
  await page.getByRole("button", { name: "Open folder", exact: true }).click();
  await expect.poll(() => calls(page, "OpenSetupBackupRecoveryFolder")).toBe(1);
  await expect(page.getByText("Desktop may sync to OneDrive", { exact: false })).toBeVisible();
});
