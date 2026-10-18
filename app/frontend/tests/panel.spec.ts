import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const calls = (page: Page, method: Parameters<Window["careTest"]["failNext"]>[0]) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).length, method);
const overview = (page: Page) => page.locator('.panel-page[aria-label="Overview"]');
const homeAccess = (page: Page) => page.locator('.panel-page[aria-label="Home access"]');
const clinic = (page: Page) => overview(page).getByRole("region", { name: "Clinic status", exact: true });
const section = (page: Page, name: string) => page.getByRole("navigation", { name: "Clinic sections" })
  .getByRole("button", { name: new RegExp(`^${name}`) });
const desktop = (page: Page) => page.getByRole("region", { name: "CARE Clinic application", exact: true });
const details = (page: Page) => overview(page).getByRole("region", { name: "Clinic details", exact: true });
const network = (page: Page) => overview(page).getByRole("region", { name: "Network profile", exact: true });

async function openPanel(page: Page, scenario = "panel-current", query = "") {
  await page.goto(`/tests/fixtures/index.html?scenario=${scenario}${query}`);
  await expect(overview(page).getByRole("heading", { name: "Overview", exact: true })).toBeVisible();
  await expect(clinic(page).getByRole("heading", { name: "Running", exact: true })).toBeVisible();
}

async function fits(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const body = page.locator(".care-panel-content");
  expect(await body.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
}

async function capture(page: Page, name: string) {
  if (!process.env.CARE_SCREENSHOTS_DIR) return;
  await mkdir(process.env.CARE_SCREENSHOTS_DIR, { recursive: true });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(process.env.CARE_SCREENSHOTS_DIR, `${name}.png`), animations: "disabled" });
}

test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.send('{"type":"connected"}'));
  page.on("pageerror", (error) => { throw error; });
});

for (const viewport of [{ width: 1100, height: 700 }, { width: 720, height: 560 }]) {
  test(`panel layouts and real QR fit ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await openPanel(page);
    await expect(page.locator(".care-panel-rail")).toHaveCount(1);
    await expect(network(page)).toHaveCount(0);
    await fits(page);
    await capture(page, `panel-actual-overview-${viewport.width}`);

    const connect = overview(page).getByRole("button", { name: "Connect a phone or tablet", exact: true });
    await connect.click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog.getByRole("img", { name: "QR code for http://care.local/setup" })).toBeVisible();
    expect(await dialog.locator("svg[role=img] path").last().getAttribute("d")).toMatch(/M/);
    await expect(dialog.getByText("http://care.local/setup", { exact: true })).toBeVisible();
    await capture(page, `panel-actual-qr-${viewport.width}`);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(connect).toBeFocused();

    await section(page, "Updates").click();
    await expect(desktop(page).getByRole("heading", { name: "CARE Clinic application" })).toBeVisible();
    await expect(page.getByRole("region", { name: "CARE software", exact: true })).toHaveCount(0);
    await fits(page);
    await capture(page, `panel-actual-updates-${viewport.width}`);
  });
}

test("start, stop and restart stay busy until native completion and ignore double clicks", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => {
    window.careTest.fixtures.finishJobs = false;
    window.careTest.hold("ClinicAction");
  });
  await clinic(page).getByRole("button", { name: "Stop", exact: true }).dblclick();
  await expect.poll(() => calls(page, "ClinicAction")).toBe(1);
  await expect(clinic(page).getByRole("heading", { name: "Stopping", exact: true })).toBeVisible();
  await page.evaluate(() => window.careTest.release("ClinicAction"));
  await expect(clinic(page).getByRole("heading", { name: "Stopping", exact: true })).toBeVisible();
  await page.evaluate(() => window.careTest.finishJob("stop"));
  await expect(clinic(page).getByRole("heading", { name: "Stopped", exact: true })).toBeVisible();
  await capture(page, "panel-actual-stopped-1100");
  await expect(overview(page).getByRole("button", { name: "Open CARE", exact: true })).toBeDisabled();
  await expect(overview(page).getByRole("button", { name: "Copy", exact: true })).toBeEnabled();
  await page.evaluate(() => window.careTest.hold("ClinicAction"));
  await clinic(page).getByRole("button", { name: "Start clinic", exact: true }).dblclick();
  await expect.poll(() => calls(page, "ClinicAction")).toBe(2);
  await page.evaluate(() => window.careTest.release("ClinicAction"));
  await expect(clinic(page).getByRole("heading", { name: "Starting", exact: true })).toBeVisible();
  await page.evaluate(() => window.careTest.finishJob("start"));
  await expect(clinic(page).getByRole("heading", { name: "Running", exact: true })).toBeVisible();
});

test("native action rejection is actionable, not a success or a technical error dump", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => window.careTest.failNext("ClinicAction", "/private/patient-path: EACCES exit status 1"));
  await clinic(page).getByRole("button", { name: "Restart", exact: true }).click();
  await expect(page.getByRole("alert").filter({ hasText: "CARE couldn't restart" })).toBeVisible();
  await expect(page.locator(".care-panel")).not.toContainText("/private/patient-path");
  await expect(page.locator(".care-panel")).not.toContainText("exit status");
  await expect(clinic(page).getByRole("button", { name: "Restart", exact: true })).toBeEnabled();
  expect(await page.evaluate(() => window.careTest.logs.some((line) => line.includes("EACCES")))).toBe(true);
});

test("an asynchronous action failure leaves a persistent readable message", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await clinic(page).getByRole("button", { name: "Restart", exact: true }).click();
  await expect(clinic(page).getByRole("heading", { name: "Restarting", exact: true })).toBeVisible();
  await capture(page, "panel-actual-restarting-1100");
  await page.evaluate(() => window.careTest.finishJob("restart", "docker compose: private technical reason"));
  await expect(page.getByRole("alert").filter({ hasText: "CARE couldn't restart" })).toBeVisible();
  await expect(page.locator(".care-panel")).not.toContainText("private technical reason");
  await expect(clinic(page).getByRole("heading", { name: "Running", exact: true })).toBeVisible();
  await capture(page, "panel-actual-action-error-1100");
});

test("service details show the engine, CARE, network name and listeners", async ({ page }) => {
  await openPanel(page);
  await expect(details(page)).toContainText("Running");
  await expect(details(page)).toContainText("1.2.3");
  await expect(details(page)).toContainText("care.local");
  await expect(details(page)).toContainText("Advertising");
  await expect(details(page)).toContainText("Port :443");
  await expect(details(page)).toContainText("Listening");
  expect(await calls(page, "ServiceStatus")).toBeGreaterThan(0);
});

test("a stopped clinic offers Start and keeps the address copyable", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-stopped");
  await expect(clinic(page).getByRole("heading", { name: "Stopped", exact: true })).toBeVisible();
  await expect(clinic(page)).toContainText("Staff can't use CARE until it's started.");
  await expect(clinic(page).getByRole("button", { name: "Start clinic", exact: true })).toBeEnabled();
  await expect(clinic(page).getByRole("button", { name: "Stop", exact: true })).toHaveCount(0);
  await expect(overview(page).getByRole("button", { name: "Open CARE", exact: true })).toBeDisabled();
  await expect(overview(page).getByRole("button", { name: "Copy", exact: true })).toBeEnabled();
  await clinic(page).getByRole("button", { name: "Start clinic", exact: true }).click();
  await expect(clinic(page).getByRole("heading", { name: "Running", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.careTest.calls.filter((call) => call.method === "ClinicAction").map((call) => call.args)))
    .toEqual([["start"]]);
});

test("an unreachable service is explained and clinic actions are locked until it answers", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-unreachable");
  await expect(clinic(page).getByRole("heading", { name: "Service not responding", exact: true })).toBeVisible();
  await expect(clinic(page)).toContainText("connection refused");
  await expect(clinic(page).getByRole("button", { name: "Start clinic", exact: true })).toBeDisabled();
  await expect(clinic(page).getByRole("button", { name: "Restart", exact: true })).toBeDisabled();
  await expect(details(page)).toContainText("isn't answering");
  await page.evaluate(() => { window.careTest.fixtures.reachable = true; });
  await clinic(page).getByRole("button", { name: "Check again", exact: true }).click();
  await expect(clinic(page).getByRole("heading", { name: "Running", exact: true })).toBeVisible();
  await expect(clinic(page).getByRole("button", { name: "Restart", exact: true })).toBeEnabled();
  expect(await calls(page, "ClinicAction")).toBe(0);
});

test("pushed service-status events update the overview", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => {
    window.careTest.fixtures.running = false;
    window.careTest.emitService();
  });
  await expect(clinic(page).getByRole("heading", { name: "Stopped", exact: true })).toBeVisible();
  await page.evaluate(() => window.careTest.emit("service-status", {
    installed: true, reachable: true,
    summary: { level: "error", title: "CARE stopped unexpectedly", detail: "The engine crashed.", running: false, notes: ["Restarting in 30 seconds"] },
  }));
  await expect(clinic(page).getByRole("heading", { name: "CARE stopped unexpectedly", exact: true })).toBeVisible();
  await expect(clinic(page)).toContainText("Restarting in 30 seconds");
});

test("a public Windows network is flagged and can be fixed from Overview", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=panel-network");
  await expect(network(page)).toBeVisible();
  await network(page).getByRole("button", { name: "Set this network to Private", exact: true }).click();
  await expect(network(page)).toHaveCount(0);
  expect(await calls(page, "FixNetwork")).toBe(1);
});

test("clipboard rejection never shows the copied toast", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => Object.defineProperty(navigator.clipboard, "writeText", {
    configurable: true, value: async () => { throw new Error("permission denied: internal clipboard details"); },
  }));
  await overview(page).getByRole("button", { name: "Copy", exact: true }).click();
  await expect(overview(page).getByRole("alert")).toContainText("Couldn't copy the address");
  await expect(page.getByText("Address copied", { exact: true })).toHaveCount(0);
  await expect(overview(page)).not.toContainText("internal clipboard details");
});

test("QR copy uses the working mobile setup URL", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => Object.defineProperty(navigator.clipboard, "writeText", {
    configurable: true, value: async (text: string) => { window.careTest.logs.push(`clipboard:${text}`); },
  }));
  await overview(page).getByRole("button", { name: "Connect a phone or tablet", exact: true }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Copy address", exact: true }).click();
  expect(await page.evaluate(() => window.careTest.logs.includes("clipboard:http://care.local/setup"))).toBe(true);
  await page.getByRole("alertdialog").getByRole("button", { name: "Done", exact: true }).click();
});

test("home access has its own tab and is not on Overview", async ({ page }) => {
  await openPanel(page);
  await expect(overview(page).getByRole("region", { name: "Home access", exact: true })).toHaveCount(0);
  const tabs = page.getByRole("navigation", { name: "Clinic sections" }).getByRole("button");
  await expect(tabs).toHaveText(["Overview", "Home access", "Updates", "Backups", "Advanced"]);
  await expect(homeAccess(page)).toBeHidden();
  await section(page, "Home access").click();
  await expect(section(page, "Home access")).toHaveAttribute("aria-current", "page");
  await expect(homeAccess(page).getByRole("heading", { name: "Home access", exact: true, level: 1 })).toBeVisible();
  await expect(homeAccess(page).getByText("Let doctors reach CARE from home with Tailscale.", { exact: true })).toBeVisible();
  const card = homeAccess(page).getByRole("region", { name: "Home access", exact: true });
  await expect(card.getByRole("heading", { name: "Let doctors use CARE from home", exact: true })).toBeVisible();
  await expect(overview(page)).toBeHidden();
  await fits(page);
});

test("home access guides Tailscale setup, turns on, shows doctor instructions and turns off", async ({ page }) => {
  await openPanel(page);
  await section(page, "Home access").click();
  const card = homeAccess(page).getByRole("region", { name: "Home access", exact: true });
  await expect(card.getByRole("heading", { name: "Let doctors use CARE from home", exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: "Get Tailscale", exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: "Turn on home access", exact: true })).toHaveCount(0);

  await page.evaluate(() => Object.assign(window.careTest.fixtures.homeAccess, {
    installed: true, running: true, https: false, host: "care.tail1234.ts.net", url: "https://care.tail1234.ts.net:8443/", ip: "100.64.0.1",
    detail: "Turn on HTTPS certificates in the Tailscale admin console.",
  }));
  await card.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(card.getByRole("heading", { name: "Turn on HTTPS in Tailscale", exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: "Open Tailscale settings", exact: true })).toBeVisible();

  await page.evaluate(() => Object.assign(window.careTest.fixtures.homeAccess, { https: true, detail: "" }));
  await card.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(card.getByRole("heading", { name: "Home access is off", exact: true })).toBeVisible();
  await card.getByRole("button", { name: "Turn on home access", exact: true }).click();
  await expect(card.getByRole("heading", { name: "Home access is on", exact: true })).toBeVisible();
  await expect(card.getByText("https://care.tail1234.ts.net:8443/", { exact: true })).toBeVisible();
  expect(await calls(page, "EnableHomeAccess")).toBe(1);

  await card.getByRole("button", { name: "Doctor instructions", exact: true }).click();
  const dialog = page.getByRole("alertdialog", { name: "Use CARE from home" });
  await expect(dialog.getByRole("img", { name: "QR code for https://care.tail1234.ts.net:8443/" })).toBeVisible();
  await fits(page);
  await dialog.getByRole("button", { name: "Done", exact: true }).click();

  await card.getByRole("button", { name: "Turn off", exact: true }).click();
  await expect(card.getByRole("heading", { name: "Home access is off", exact: true })).toBeVisible();
  expect(await calls(page, "DisableHomeAccess")).toBe(1);
});

test("home access failure is readable and leaves the card usable", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => Object.assign(window.careTest.fixtures.homeAccess, {
    installed: true, running: true, https: true, host: "care.tail1234.ts.net", url: "https://care.tail1234.ts.net:8443/", detail: "",
  }));
  await section(page, "Home access").click();
  const card = homeAccess(page).getByRole("region", { name: "Home access", exact: true });
  await card.getByRole("button", { name: "Check again", exact: true }).click();
  await page.evaluate(() => window.careTest.failNext("EnableHomeAccess", "tailscale serve: permission denied"));
  await card.getByRole("button", { name: "Turn on home access", exact: true }).click();
  await expect(card.getByRole("alert")).toContainText("Couldn't turn on home access");
  await expect(card.getByRole("button", { name: "Turn on home access", exact: true })).toBeEnabled();
});

test("home access says so when Tailscale's sharing settings can't be read", async ({ page }) => {
  await openPanel(page);
  await page.evaluate(() => Object.assign(window.careTest.fixtures.homeAccess, {
    installed: true, running: true, https: true, host: "care.tail1234.ts.net", url: "https://care.tail1234.ts.net:8443/",
    unknown: true, detail: "Couldn't read Tailscale's sharing settings. Open Tailscale and check that it is running.",
  }));
  await section(page, "Home access").click();
  const card = homeAccess(page).getByRole("region", { name: "Home access", exact: true });
  await card.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(card.getByRole("heading", { name: "Couldn't check home access", exact: true })).toBeVisible();
  await expect(card.getByText("Couldn't read Tailscale's sharing settings.", { exact: false })).toBeVisible();
  await expect(card.getByRole("heading", { name: "Home access is off", exact: true })).toHaveCount(0);
  await expect(card.getByRole("button", { name: "Turn on home access", exact: true })).toHaveCount(0);
  await page.evaluate(() => Object.assign(window.careTest.fixtures.homeAccess, { unknown: false, detail: "" }));
  await card.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(card.getByRole("heading", { name: "Home access is off", exact: true })).toBeVisible();
});
