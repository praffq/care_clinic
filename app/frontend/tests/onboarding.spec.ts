import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";

const samplePassword = "ClinicTest123";
const forward = (page: Page) => page.getByRole("button", { name: "Continue", exact: true });
const install = (page: Page) => page.getByRole("button", { name: "Install", exact: true });
const calls = (page: Page, method: string) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).length, method);

test.beforeEach(async ({ page, baseURL }) => {
  // Keep an in-flight flow stable when another agent edits this shared checkout.
  const socketOrigin = new URL(baseURL ?? "http://127.0.0.1:41783").origin.replace(/^http/, "ws");
  await page.routeWebSocket(`${socketOrigin}/**`, (socket) => socket.send('{"type":"connected"}'));
  page.on("pageerror", (e) => { throw e; });
});

async function start(page: Page, query = "") {
  await page.goto(`/tests/fixtures/index.html?scenario=current${query}`);
  await expect(page.getByRole("heading", { name: "Set up CARE on this computer" })).toBeVisible();
}
async function client(page: Page) {
  await start(page);
  await page.getByRole("button", { name: "Connect to an existing server on the local network" }).click();
  await expect(page.getByRole("heading", { name: "Find your clinic's server" })).toBeVisible();
}
async function continueTo(page: Page, title = "Choosing the clinic address") {
  const heading = page.locator("#setup-title");
  await expect(heading).toBeVisible();
  for (let step = 0; step < 6; step++) {
    const current = await heading.innerText();
    if (current === title) return;
    await expect(forward(page)).toBeEnabled();
    await forward(page).click();
    await expect(heading).not.toHaveText(current);
  }
  await expect(heading).toHaveText(title);
}
async function address(page: Page, platform = "darwin") {
  await page.goto(`/tests/fixtures/index.html?scenario=current&role=server&platform=${platform}`);
  await continueTo(page);
  await expect(page.getByRole("heading", { name: "Choosing the clinic address" })).toBeVisible();
  await expect(forward(page)).toBeEnabled();
}
async function admin(page: Page, platform = "darwin") {
  await address(page, platform);
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Creating the admin password" })).toBeVisible();
}
async function fillPassword(page: Page, password = samplePassword) {
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
}

test("admin setup reminds the user to save the password securely", async ({ page }) => {
  await admin(page);
  await expect(page.getByText("Save this password in your password manager or write it down somewhere secure before starting installation.")).toBeVisible();
  await expect(page.getByRole("button", { name: /copy.*password/i })).toHaveCount(0);
});

async function review(page: Page, platform = "darwin") {
  await admin(page, platform);
  await fillPassword(page);
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Review before installing" })).toBeVisible();
  await expect(install(page)).toBeEnabled();
}
async function capture(page: Page, name: string) {
  if (!process.env.CARE_SCREENSHOTS_DIR) return;
  await mkdir(process.env.CARE_SCREENSHOTS_DIR, { recursive: true });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(process.env.CARE_SCREENSHOTS_DIR, `${name}.png`), animations: "disabled" });
}
async function fits(page: Page) {
  expect(await page.evaluate(() => {
    const root = document.querySelector(".onboarding-flow")!;
    const footer = document.querySelector(".on-foot")?.getBoundingClientRect();
    return {
      pageX: document.documentElement.scrollWidth > innerWidth,
      pageY: document.documentElement.scrollHeight > innerHeight,
      rootX: root.scrollWidth > root.clientWidth,
      innerX: [...root.querySelectorAll(".on-main, .on-body, .on-client-body, .on-rail, .on-left")]
        .some((element) => element.scrollWidth > element.clientWidth + 1),
      footer: !footer || (footer.left >= 0 && footer.right <= innerWidth + 1 && footer.bottom <= innerHeight + 1),
    };
  })).toEqual({ pageX: false, pageY: false, rootX: false, innerX: false, footer: true });
}

test("find is read-only, single-flight, and requires confirmation before connecting", async ({ page }) => {
  await client(page);
  await expect(page.getByLabel("Clinic address")).toBeFocused();
  await page.evaluate(() => window.careTest.hold("FindClinic"));
  await page.getByRole("button", { name: "Find server", exact: true }).evaluate((button: HTMLButtonElement) => { button.click(); button.click(); });
  await expect(page.getByText("Looking for care.local on your network…")).toBeVisible();
  await expect(page.getByRole("button", { name: "Back", exact: true })).toBeDisabled();
  expect(await calls(page, "FindClinic")).toBe(1);
  expect(await calls(page, "ConnectClient")).toBe(0);
  expect(await page.evaluate(() => window.careTest.state.role)).toBe("");
  await page.evaluate(() => window.careTest.release("FindClinic"));
  await expect(page.getByRole("heading", { name: "We found your clinic's server" })).toBeVisible();
  expect(await calls(page, "SelectRole")).toBe(0);
  await page.getByRole("button", { name: "Not this one" }).click();
  await expect(page.getByLabel("Clinic address")).toHaveValue("care");
  expect(await calls(page, "ConnectClient")).toBe(0);
});

for (const value of ["https://care.local:8000/login", "care.local/login", "care?x=1", "-clinic", "clinic-", "a".repeat(64)]) {
  test(`client rejects an invalid name: ${value.slice(0, 24)}`, async ({ page }) => {
    await client(page);
    await page.getByLabel("Clinic address").fill(value);
    await expect(page.getByRole("button", { name: "Find server" })).toBeDisabled();
    await page.getByLabel("Clinic address").press("Enter");
    expect(await calls(page, "FindClinic")).toBe(0);
    expect(await calls(page, "ConnectClient")).toBe(0);
  });
}

test("pasting a valid full clinic address strips only the supported wrapper", async ({ page }) => {
  await client(page);
  await page.getByLabel("Clinic address").evaluate((element) => {
    const clipboardData = new DataTransfer();
    clipboardData.setData("text/plain", "https://CARE-HOSPITAL.local/");
    element.dispatchEvent(new ClipboardEvent("paste", { clipboardData, bubbles: true, cancelable: true }));
  });
  await expect(page.getByLabel("Clinic address")).toHaveValue("care-hospital");
  await page.getByRole("button", { name: "Find server" }).click();
  await expect(page.getByRole("heading", { name: "We found your clinic's server" })).toBeVisible();
  expect(await page.evaluate(() => window.careTest.calls.find((call) => call.method === "FindClinic")?.args)).toEqual(["care-hospital.local"]);
});

for (const first of ["find", "update"] as const) {
  test(`client discovery and updating cannot race when ${first} starts first`, async ({ page }) => {
    await page.goto("/tests/fixtures/index.html?scenario=available&role=client");
    await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeEnabled();
    await page.evaluate((action) => {
      window.careTest.hold("FindClinic");
      const button = (text: string) => [...document.querySelectorAll("button")].find((item) => item.textContent?.trim() === text)!;
      const find = button("Find server");
      const update = button("Update now");
      if (action === "find") { find.click(); update.click(); }
      else { update.click(); find.click(); }
    }, first);
    if (first === "find") {
      await expect.poll(() => calls(page, "FindClinic")).toBe(1);
      expect(await calls(page, "DownloadAppUpdate")).toBe(0);
      await page.evaluate(() => window.careTest.release("FindClinic"));
      await expect(page.getByRole("heading", { name: "We found your clinic's server" })).toBeVisible();
    } else {
      await expect(page.getByText("Downloading CARE Clinic 0.1.6…", { exact: true })).toBeVisible();
      expect(await calls(page, "FindClinic")).toBe(0);
      expect(await calls(page, "ClientPreflight")).toBe(0);
    }
  });
}

for (const role of ["client", "server"] as const) {
  for (const phase of ["installer", "restarting"] as const) {
    test(`terminal ${phase} app-update keeps ${role} locked until ${phase === "installer" ? "acknowledgement" : "reopening"}`, async ({ page }) => {
      await page.setViewportSize({ width: 720, height: 560 });
      await page.goto(`/tests/fixtures/index.html?scenario=available&role=${role}`);
      if (role === "server") await continueTo(page);
      const next = role === "client" ? page.getByRole("button", { name: "Find server", exact: true }) : forward(page);
      await expect(next).toBeEnabled();
      await page.getByRole("button", { name: "Update now", exact: true }).click();
      await expect(page.getByText("Downloading CARE Clinic 0.1.6…", { exact: true })).toBeVisible();
      await page.evaluate((value) => {
        window.careTest.progress({ phase: value, done: 0, total: 0 });
        window.careTest.finishUpdate();
      }, phase);
      const guidance = page.getByText(phase === "installer"
        ? "Follow it to finish updating, then reopen CARE Clinic."
        : "You'll be back here in a moment.", { exact: true });
      await expect(guidance).toBeVisible();
      await expect(next).toBeDisabled();
      await expect(page.getByRole("button", { name: "Back", exact: true })).toBeDisabled();
      await expect(page.getByLabel("Clinic address", { exact: true })).toBeDisabled();
      if (phase === "installer") {
        await expect(page.getByRole("button", { name: "OK", exact: true })).toBeEnabled();
      } else {
        await expect(page.getByRole("button", { name: /^(OK|Done|Dismiss|Later)$/ })).toHaveCount(0);
      }
      await next.evaluate((button: HTMLButtonElement) => button.click());
      for (const method of ["FindClinic", "ConnectClient", "RunSetup"]) expect(await calls(page, method)).toBe(0);
      await guidance.scrollIntoViewIfNeeded();
      await fits(page);
      await capture(page, `onboarding-${role}-update-${phase}-720x560`);
      if (phase === "installer") {
        await page.getByRole("button", { name: "OK", exact: true }).click();
        await expect(next).toBeEnabled();
        await expect(page.getByLabel("Clinic address", { exact: true })).toBeEnabled();
        await expect(page.getByText("Up to date", { exact: true })).toHaveCount(0);
      }
    });
  }
}

test("connection progress comes from host events and rejection is not connected", async ({ page }) => {
  await client(page);
  await page.getByRole("button", { name: "Find server" }).click();
  await expect(page.getByRole("button", { name: "Connect", exact: true })).toBeVisible();
  await page.evaluate(() => {
    window.careTest.hold("ConnectClient");
    window.careTest.failNext("ConnectClient", "could not install the clinic certificate: User canceled (-128)");
  });
  await page.getByRole("button", { name: "Connect", exact: true }).evaluate((button: HTMLButtonElement) => { button.click(); button.click(); });
  await expect(page.getByRole("heading", { name: "Connecting to care.local" })).toBeVisible();
  expect(await calls(page, "ConnectClient")).toBe(1);
  await page.evaluate(() => window.careTest.emit("client-connect-progress", "checking"));
  await expect(page.getByText("Making sure CARE opens safely")).toBeVisible();
  await expect(page.getByRole("heading", { name: "You're connected" })).toHaveCount(0);
  await page.evaluate(() => window.careTest.release("ConnectClient"));
  await expect(page.getByRole("alert")).toContainText("Permission was not given");
  await expect(page.getByText(/-128|User canceled/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Back", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Try again", exact: true }).click();
  await expect(page.getByRole("heading", { name: "You're connected" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open CARE", exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: "Back", exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => window.careTest.state.client_url)).toBe("https://care.local");
});

for (const [raw, title] of [
  ["could not reach the clinic: dial 192.0.2.1: no route", "We couldn't find the clinic"],
  ["this is not a CARE clinic root certificate", "This doesn't look like a CARE clinic"],
  ["the clinic certificate is not valid now: internal details", "Check this computer's date and time"],
  ["could not verify the secure connection: x509 private details", "We couldn't connect securely"],
  ["could not save the clinic connection; no certificate was installed: EACCES", "The connection couldn't be saved"],
  ["something else is still running - private job", "Please wait a moment"],
]) {
  test(`client maps ${title} without exposing technical causes`, async ({ page }) => {
    await client(page);
    await page.evaluate((error) => window.careTest.failNext("FindClinic", error), raw);
    await page.getByRole("button", { name: "Find server" }).click();
    await expect(page.getByRole("alert")).toContainText(title);
    expect(await page.locator("body").innerText()).not.toContain(raw);
    expect(await calls(page, "ConnectClient")).toBe(0);
  });
}

test("unfinished server setup points to Setup and never runs client cleanup", async ({ page }) => {
  await client(page);
  await page.evaluate(() => {
    window.careTest.fixtures.preflight.unfinished_server_setup = true;
    window.careTest.state.role = "server";
  });
  await page.getByRole("button", { name: "Find server" }).click();
  await expect(page.getByRole("alert")).toContainText("This computer has an unfinished clinic setup");
  expect(await calls(page, "FindClinic")).toBe(0);
  await page.getByRole("button", { name: "Open setup" }).click();
  await expect(page.getByRole("heading", { name: "Checking this computer" })).toBeVisible();
  expect(await calls(page, "BeginServerSetup")).toBe(1);
});

test("an old self-address setting offers the backend repair path", async ({ page }) => {
  await client(page);
  await page.evaluate(() => {
    window.careTest.fixtures.preflight.hosts_entry = true;
    window.careTest.failNext("FindClinic", "this computer is sending the clinic address to itself; connect to fix it");
  });
  await page.getByRole("button", { name: "Find server" }).click();
  await page.getByRole("button", { name: "Connect and fix" }).click();
  await expect(page.getByRole("heading", { name: "You're connected" })).toBeVisible();
});

test("saved clients poll reachability and recover without losing the connection", async ({ page }) => {
  await page.clock.install();
  await page.goto("/tests/fixtures/index.html?scenario=client-saved");
  await expect(page.getByRole("button", { name: "Open CARE" })).toBeEnabled();
  await page.evaluate(() => {
    window.careTest.fixtures.reachability.reachable = false;
    window.careTest.fixtures.reachability.detail = "the server did not answer";
  });
  await page.clock.fastForward(15_100);
  await expect(page.getByText("This computer is set up for care.local, but we can't find it right now")).toBeVisible();
  await expect(page.getByRole("button", { name: "Open CARE" })).toBeDisabled();
  expect(await page.evaluate(() => window.careTest.state.client_url)).toBe("https://care.local");
  await page.evaluate(() => { window.careTest.fixtures.reachability.reachable = true; });
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("button", { name: "Open CARE" })).toBeEnabled();
  expect(await calls(page, "DisconnectClient")).toBe(0);
});

test("an unavailable clipboard shows a friendly error without throwing or losing the saved clinic", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=client-saved");
  await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined }));
  await page.getByRole("button", { name: "Copy address", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("Couldn't copy the address. You can select it and copy it yourself.");
  expect(await page.evaluate(() => window.careTest.state.client_url)).toBe("https://care.local");
  expect(await page.evaluate(() => window.careTest.logs.some((line) => line.includes("copy clinic address:")))).toBe(true);
  expect(await calls(page, "DisconnectClient")).toBe(0);
});

test("a failed post-disconnect navigation does not leave a false saved connection on screen", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=client-saved");
  await page.evaluate(() => window.careTest.failNext("ClearRole", "private native failure"));
  await page.getByRole("button", { name: "Disconnect", exact: true }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Disconnect", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Find your clinic's server" })).toBeVisible();
  await expect(page.getByLabel("Clinic address")).toBeFocused();
  await expect(page.getByRole("button", { name: "Open CARE", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => window.careTest.state.client_url)).toBe("");
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Set up CARE on this computer" })).toBeVisible();
});

test("disconnect confirms, survives permission failure, and never deletes clinic data", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=client-saved");
  await page.getByRole("button", { name: "Disconnect", exact: true }).click();
  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("No patient or clinic data is deleted");
  expect(await calls(page, "DisconnectClient")).toBe(0);
  await page.evaluate(() => window.careTest.failNext("DisconnectClient", "could not remove the clinic certificate: permission denied"));
  await dialog.getByRole("button", { name: "Disconnect", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("Couldn't disconnect this computer");
  expect(await page.evaluate(() => window.careTest.state.client_url)).toBe("https://care.local");
  await dialog.getByRole("button", { name: "Disconnect", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Set up CARE on this computer" })).toBeVisible();
  for (const method of ["PurgeResidue", "RunUninstall", "RemoveApp"]) expect(await calls(page, method)).toBe(0);
});

test("a partially saved connection stays removable without claiming connection success", async ({ page }) => {
  await client(page);
  await page.getByRole("button", { name: "Find server" }).click();
  await page.evaluate(() => {
    window.careTest.state.role = "client";
    window.careTest.state.client_url = "https://care.local";
    window.careTest.failNext("ConnectClient", "could not install the clinic certificate: permission denied");
  });
  await page.getByRole("button", { name: "Connect", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("Your computer didn't allow the connection");
  await expect(page.getByRole("heading", { name: "You're connected" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Back", exact: true }).click();
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeVisible();
  expect(await page.evaluate(() => window.careTest.state.role)).toBe("client");
});

test("an address field disabled during entry receives focus after its asynchronous check", async ({ page }) => {
  await start(page);
  await page.evaluate(() => window.careTest.hold("MDNSStatus"));
  await page.getByRole("button", { name: "Start setup" }).click();
  await continueTo(page);
  const input = page.getByLabel("Clinic address", { exact: true });
  await expect(input).toBeVisible();
  await expect(input).toBeDisabled();
  await page.evaluate(() => window.careTest.release("MDNSStatus"));
  await expect(input).toBeEnabled();
  await expect(input).toBeFocused();
});

test("address checks keep focus, discard stale answers, and never save a taken name", async ({ page }) => {
  await address(page);
  const input = page.getByLabel("Clinic address", { exact: true });
  await expect(input).toBeFocused();
  const count = await calls(page, "SetMDNSName");
  await page.evaluate(() => { window.careTest.fixtures.addressTaken = true; });
  await input.fill("care-hospital");
  await expect(page.getByText("care-hospital.local is already taken on this network — type a different name.")).toBeVisible();
  await expect(input).toBeFocused();
  await expect(forward(page)).toBeDisabled();
  expect(await calls(page, "SetMDNSName")).toBe(count);
  await page.evaluate(() => {
    window.careTest.fixtures.addressTaken = false;
    window.careTest.hold("MDNSStatus");
  });
  await input.fill("first");
  await expect.poll(() => calls(page, "MDNSStatus")).toBeGreaterThan(2);
  await input.fill("second");
  await page.waitForTimeout(400);
  await page.evaluate(() => window.careTest.release("MDNSStatus"));
  await expect(forward(page)).toBeEnabled();
  expect(await page.evaluate(() => window.careTest.state.mdns_name)).toBe("second.local");
  expect(await page.evaluate(() => window.careTest.calls.filter((call) => call.method === "SetMDNSName").some((call) => call.args[0] === "first.local"))).toBe(false);
});

test("address saves are ordered, so an older in-flight save cannot overwrite the new name", async ({ page }) => {
  await address(page);
  const baseline = await calls(page, "SetMDNSName");
  const checked = await calls(page, "MDNSStatus");
  await page.evaluate(() => window.careTest.hold("SetMDNSName"));
  await page.getByLabel("Clinic address", { exact: true }).fill("first");
  await expect.poll(() => calls(page, "SetMDNSName")).toBe(baseline + 1);
  await page.getByLabel("Clinic address", { exact: true }).fill("second");
  await expect.poll(() => calls(page, "MDNSStatus")).toBe(checked + 2);
  expect(await calls(page, "SetMDNSName")).toBe(baseline + 1);
  await expect(forward(page)).toBeDisabled();
  await page.evaluate(() => window.careTest.release("SetMDNSName"));
  await expect(forward(page)).toBeEnabled();
  expect(await calls(page, "SetMDNSName")).toBe(baseline + 2);
  expect(await page.evaluate(() => window.careTest.state.mdns_name)).toBe("second.local");
});

test("address check failures offer a log without putting technical causes on screen", async ({ page }) => {
  await address(page);
  await page.evaluate(() => window.careTest.failNext("MDNSStatus", "private multicast lookup failed"));
  await page.getByLabel("Clinic address", { exact: true }).fill("care-hospital");
  await expect(page.getByText("Couldn't check or save this clinic address. Try again, or share the log file with your support contact.")).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await page.getByRole("button", { name: "Open log file", exact: true }).click();
  expect(await calls(page, "OpenLogFolder")).toBe(1);
  expect(await page.locator("body").innerText()).not.toContain("private multicast lookup failed");
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(forward(page)).toBeEnabled();
});

test("typing an address blocks an update even before the debounced check starts", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=available&role=server");
  await continueTo(page);
  await expect(forward(page)).toBeEnabled();
  await page.evaluate(() => {
    const input = document.querySelector<HTMLInputElement>("#mdnsname")!;
    const update = [...document.querySelectorAll("button")].find((item) => item.textContent?.trim() === "Update now")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "care-hospital");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    update.click();
  });
  await expect(forward(page)).toBeEnabled();
  expect(await calls(page, "DownloadAppUpdate")).toBe(0);
  expect(await page.evaluate(() => window.careTest.state.mdns_name)).toBe("care-hospital.local");
});

test("a synchronous invalid-address retry releases the update lock", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=available&role=server");
  await continueTo(page);
  await expect(forward(page)).toBeEnabled();
  await page.getByLabel("Clinic address", { exact: true }).fill("-");
  await page.getByRole("button", { name: "Check again", exact: true }).click();
  await expect(page.getByRole("button", { name: "Update now", exact: true })).toBeEnabled();
  await expect(page.getByLabel("Clinic address", { exact: true })).toBeEnabled();
  await expect(forward(page)).toBeDisabled();
});

test("password validity resets immediately when input changes and stale validation cannot pass", async ({ page }) => {
  await admin(page);
  await fillPassword(page);
  await expect(forward(page)).toBeEnabled();
  await page.evaluate(() => window.careTest.hold("ValidatePassword"));
  await fillPassword(page, "weak");
  await expect(forward(page)).toBeDisabled();
  await page.waitForTimeout(250);
  await page.evaluate(() => window.careTest.release("ValidatePassword"));
  await expect(page.getByText("Password must be at least 8 characters.")).toBeVisible();
  await expect(forward(page)).toBeDisabled();
});

test("password validator failures never log submitted credentials or host secrets", async ({ page }) => {
  await admin(page);
  const privateError = `Validation failed for ${samplePassword}; API_TOKEN=preview-secret`;
  await page.evaluate((reason) => window.careTest.failNext("ValidatePassword", reason), privateError);
  await fillPassword(page);
  await expect(page.getByText("Couldn't check the password. Try typing it again.", { exact: true })).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  const logs = await page.evaluate(() => window.careTest.logs);
  expect(logs).toContain("password validation failed");
  for (const secret of [samplePassword, "preview-secret"]) {
    expect(logs.join("\n")).not.toContain(secret);
  }
  expect(await page.locator("body").innerText()).not.toContain(privateError);
  await fillPassword(page, "UpdatedClinic123");
  await expect(forward(page)).toBeEnabled();
});

test("a missing background service can be installed from the computer step", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=setup-service");
  await expect(page.getByRole("heading", { name: "Checking this computer" })).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await page.getByRole("button", { name: "Install service", exact: true }).click();
  await expect(forward(page)).toBeEnabled();
  expect(await calls(page, "InstallService")).toBe(1);
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Removing stale files from an earlier setup" })).toBeVisible();
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Choosing the clinic address" })).toBeVisible();
});

test("leftovers from an earlier setup block Continue until they are removed", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=setup-cleanup");
  await expect(page.getByRole("heading", { name: "Checking this computer" })).toBeVisible();
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Removing stale files from an earlier setup" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Still on this computer" })).toBeVisible();
  for (const label of ["Old clinic data", "Installed files", "Hosts file entry"]) {
    await expect(page.locator(".on-leftovers li", { hasText: label })).toBeVisible();
  }
  await expect(forward(page)).toBeDisabled();
  expect(await calls(page, "PurgeResidue")).toBe(0);
  await page.evaluate(() => window.careTest.failNext("PurgeResidue", "could not remove the hosts entry: permission denied"));
  await page.getByRole("button", { name: "Remove it all", exact: true }).click();
  await expect(page.getByText("Cleanup didn't finish.", { exact: false })).toBeVisible();
  await expect(page.locator(".on-leftovers li", { hasText: "Hosts file entry" })).toContainText("Still here");
  await expect(forward(page)).toBeDisabled();
  await page.locator(".on-card").getByRole("button", { name: "Try again", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Nothing from the earlier setup is left" })).toBeVisible();
  await expect(page.locator(".on-leftovers li", { hasText: "Old clinic data" })).toContainText("Removed");
  expect(await page.evaluate(() => window.careTest.calls.filter((call) => call.method === "PurgeResidue").map((call) => call.args))).toEqual([[true], [true]]);
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Choosing the clinic address" })).toBeVisible();
  expect(await calls(page, "RunSetup")).toBe(0);
});

test("a clean computer passes the cleanup step without removing anything", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=current&role=server");
  await continueTo(page, "Removing stale files from an earlier setup");
  await expect(page.getByRole("heading", { name: "Nothing from the earlier setup is left" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Remove it all", exact: true })).toHaveCount(0);
  await expect(forward(page)).toBeEnabled();
  expect(await calls(page, "ScanResidue")).toBeGreaterThan(0);
  expect(await calls(page, "PurgeResidue")).toBe(0);
});

test("a failing computer check blocks Continue until Re-check passes", async ({ page }) => {
  await page.goto("/tests/fixtures/index.html?scenario=setup-memory");
  await expect(page.getByRole("heading", { name: "Checking this computer" })).toBeVisible();
  await expect(page.getByText("CARE needs at least 8 GB of memory.")).toBeVisible();
  await expect(forward(page)).toBeDisabled();
  await page.evaluate(() => { window.careTest.fixtures.checks[1].level = "ok"; });
  await page.getByRole("button", { name: "Re-check", exact: true }).click();
  await expect(forward(page)).toBeEnabled();
});

for (const step of ["computer", "address", "admin"] as const) {
  test(`Review revalidates ${step} before RunSetup and stays on Review`, async ({ page }) => {
    await review(page);
    await page.evaluate((target) => {
      const f = window.careTest.fixtures;
      if (target === "computer") f.serviceInstalled = false;
      if (target === "address") window.careTest.respond("ValidateDomain", "invalid name");
      if (target === "admin") window.careTest.respond("ValidatePassword", "Password must be at least 8 characters.");
    }, step);
    await install(page).click();
    await expect(page.getByRole("heading", { name: "Review before installing" })).toBeVisible();
    await expect(page.getByRole("alert")).toContainText("One step needs another look");
    await expect(install(page)).toBeDisabled();
    expect(await calls(page, "RunSetup")).toBe(0);
  });
}

test("Review Fix returns directly and preserves the address and password", async ({ page }) => {
  await review(page);
  await page.evaluate(() => { window.careTest.fixtures.serviceInstalled = false; });
  await install(page).click();
  await page.getByRole("button", { name: "Fix This computer" }).click();
  await expect(page.getByRole("heading", { name: "Checking this computer" })).toBeVisible();
  await page.getByRole("button", { name: "Install service", exact: true }).click();
  await expect(forward(page)).toBeEnabled();
  await forward(page).click();
  await expect(page.getByRole("heading", { name: "Review before installing" })).toBeVisible();
  await expect(install(page)).toBeEnabled();
  await page.getByRole("button", { name: "Edit Admin login" }).click();
  await expect(page.getByLabel("Password", { exact: true })).toHaveValue(samplePassword);
  await expect(page.getByLabel("Confirm password", { exact: true })).toHaveValue(samplePassword);
  await page.getByRole("button", { name: "Back to review", exact: true }).first().click();
  await expect(install(page)).toBeEnabled();
});

test("RunSetup rejection before acceptance stays on Review; acceptance alone enters installation", async ({ page }) => {
  await review(page);
  await page.evaluate(() => window.careTest.failNext("RunSetup", "something else is still running - private details"));
  await install(page).click();
  await expect(page.getByRole("heading", { name: "Review before installing" })).toBeVisible();
  await expect(page.getByText("Installation hasn't started.", { exact: false })).toBeVisible();
  await page.evaluate(() => {
    window.careTest.respond("RunSetup", undefined);
    window.careTest.hold("RunSetup");
  });
  await install(page).evaluate((button: HTMLButtonElement) => { button.click(); button.click(); });
  await expect.poll(() => calls(page, "RunSetup")).toBe(2);
  await expect(install(page)).toBeDisabled();
  await page.evaluate(() => window.careTest.release("RunSetup"));
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  expect(await page.evaluate(() => {
    const attempts = window.careTest.calls.filter((call) => call.method === "RunSetup");
    return attempts[attempts.length - 1]?.args;
  })).toEqual(["care.local", samplePassword]);
});

test("installation preserves the 6-step wizard rail", async ({ page }) => {
  await review(page);
  const rail = page.getByRole("complementary", { name: "Setup progress" });
  const labels = rail.locator(".on-rail-step > span:nth-child(2)");
  await expect(rail).toContainText("Step 5 of 6");
  const reviewedPages = await labels.allTextContents();
  expect(reviewedPages).toHaveLength(6);
  await page.evaluate(() => window.careTest.respond("RunSetup", undefined));
  await install(page).click();
  await expect(page.getByRole("heading", { name: "Installing CARE", exact: true })).toBeVisible();
  await expect(rail).toContainText("Step 6 of 6");
  await expect(labels).toHaveText(reviewedPages);
  await expect(rail.locator('[aria-current="step"]')).toContainText("Install");
  await capture(page, "onboarding-install-rail");
});

for (const size of [{ width: 1100, height: 700 }, { width: 720, height: 560 }]) {
  test(`all configuration pages fit ${size.width}x${size.height}`, async ({ page }) => {
    await page.setViewportSize(size);
    await page.goto("/tests/fixtures/index.html?scenario=current&role=server");
    await expect(page.getByRole("heading", { name: "Checking this computer" })).toBeVisible();
    await expect(forward(page)).toBeEnabled();
    await fits(page); await capture(page, `onboarding-computer-${size.width}x${size.height}`);
    await forward(page).click();
    await expect(page.getByRole("heading", { name: "Removing stale files from an earlier setup" })).toBeVisible();
    await expect(forward(page)).toBeEnabled();
    await fits(page); await capture(page, `onboarding-cleanup-${size.width}x${size.height}`);
    await forward(page).click();
    await expect(page.getByRole("heading", { name: "Choosing the clinic address" })).toBeVisible();
    await expect(forward(page)).toBeEnabled();
    await fits(page); await capture(page, `onboarding-address-${size.width}x${size.height}`);
    await forward(page).click();
    await expect(page.getByRole("heading", { name: "Creating the admin password" })).toBeVisible();
    await fits(page); await capture(page, `onboarding-admin-${size.width}x${size.height}`);
    await fillPassword(page);
    await expect(forward(page)).toBeEnabled();
    await forward(page).click();
    await expect(install(page)).toBeEnabled();
    await fits(page); await capture(page, `onboarding-review-${size.width}x${size.height}`);
  });
  test(`client states fit ${size.width}x${size.height}`, async ({ page }) => {
    await page.setViewportSize(size);
    await client(page);
    await fits(page); await capture(page, `onboarding-client-entry-${size.width}x${size.height}`);
    await page.getByRole("button", { name: "Find server" }).click();
    await expect(page.getByRole("heading", { name: "We found your clinic's server" })).toBeVisible();
    await fits(page); await capture(page, `onboarding-client-found-${size.width}x${size.height}`);
    await page.evaluate(() => window.careTest.hold("ConnectClient"));
    await page.getByRole("button", { name: "Connect", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Connecting to care.local" })).toBeVisible();
    await fits(page); await capture(page, `onboarding-client-connecting-${size.width}x${size.height}`);
    await page.evaluate(() => window.careTest.emit("client-connect-progress", "checking"));
    await expect(page.getByText("Making sure CARE opens safely")).toBeVisible();
    await fits(page); await capture(page, `onboarding-client-checking-${size.width}x${size.height}`);
    await page.evaluate(() => window.careTest.release("ConnectClient"));
    await expect(page.getByRole("heading", { name: "You're connected" })).toBeVisible();
    await fits(page); await capture(page, `onboarding-client-connected-${size.width}x${size.height}`);
    await page.goto("/tests/fixtures/index.html?scenario=client-offline");
    await expect(page.getByText("This computer is set up for care.local, but we can't find it right now")).toBeVisible();
    await fits(page); await capture(page, `onboarding-client-offline-${size.width}x${size.height}`);
    await page.getByRole("button", { name: "Disconnect", exact: true }).scrollIntoViewIfNeeded();
    await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeInViewport();
    await fits(page);
  });
  test(`requirement and taken-address screens fit ${size.width}x${size.height}`, async ({ page }) => {
    await page.setViewportSize(size);
    for (const [scenario, title] of [
      ["setup-service", "Checking this computer"],
      ["setup-memory", "Checking this computer"],
      ["setup-cleanup", "Removing stale files from an earlier setup"],
      ["setup-address", "Choosing the clinic address"],
    ] as const) {
      await page.goto(`/tests/fixtures/index.html?scenario=${scenario}`);
      await continueTo(page, title);
      await expect(page.getByRole("heading", { name: title, exact: true })).toBeVisible();
      await fits(page); await capture(page, `onboarding-${scenario}-${size.width}x${size.height}`);
      await expect(forward(page)).toBeInViewport();
    }
  });
}
