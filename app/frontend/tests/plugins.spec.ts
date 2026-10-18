import { expect, test, type Page } from "@playwright/test";
import type {} from "./fixtures/host";
import { draftFor, parseLines, pluginFrom } from "../src/screens/panel/plugin-model";

const password = "ClinicTest123";
const card = (page: Page) => page.locator("section[aria-labelledby='advanced-plugins-title']");
const dialog = (page: Page) => page.getByRole("alertdialog");
const lastCall = (page: Page, method: string) => page.evaluate((name) =>
  window.careTest.calls.filter((call) => call.method === name).at(-1)?.args ?? null, method);

test.use({ trace: "off" });
test.beforeEach(async ({ page }) => {
  await page.routeWebSocket("**", (socket) => socket.close());
  await page.goto("/tests/fixtures/index.html?scenario=panel-running");
  await page.waitForFunction(() => !!window.careTest);
  await page.getByRole("button", { name: "Advanced", exact: true }).click();
  const advanced = page.locator(".care-advanced");
  await advanced.getByLabel("CARE Clinic admin password", { exact: true }).fill(password);
  await advanced.getByRole("button", { name: "Unlock", exact: true }).click();
  await expect(card(page).getByRole("list", { name: "Enabled plugins" })).toContainText("Files on every address");
});

test("adds a preset, prompting for its required settings", async ({ page }) => {
  await card(page).getByRole("button", { name: "Add Filly" }).click();
  await dialog(page).getByRole("button", { name: "Add and restart CARE" }).click();
  await expect(dialog(page).getByRole("alert")).toHaveText("Enter MEDISPEAK_API_KEY.");
  await dialog(page).getByLabel("MEDISPEAK_API_KEY", { exact: true }).fill("sk-test");
  await dialog(page).getByLabel("MEDISPEAK_BASE_URL", { exact: true }).fill("https://medispeak.example");
  await dialog(page).getByRole("button", { name: "Add and restart CARE" }).click();
  await expect(dialog(page)).toBeHidden();
  expect(await lastCall(page, "EnablePlugin")).toEqual([{
    id: "care_filly", package: "git+https://github.com/ohcnetwork/care_filly.git", version: "@main",
    configs: { MEDISPEAK_API_KEY: "sk-test", MEDISPEAK_BASE_URL: "https://medispeak.example" },
  }, password]);
  await expect(card(page).getByRole("list", { name: "Enabled plugins" })).toContainText("Filly");
  await expect(card(page).getByRole("list", { name: "Available plugins" })).toHaveCount(0);

  // Editing keeps the saved secret when its field is left blank.
  await card(page).getByRole("button", { name: "Settings for care_filly" }).click();
  await expect(dialog(page).getByLabel("MEDISPEAK_API_KEY", { exact: true })).toHaveValue("");
  await dialog(page).getByRole("button", { name: "Save and restart CARE" }).click();
  await expect(dialog(page)).toBeHidden();
  expect((await lastCall(page, "EnablePlugin"))?.[0]).toMatchObject({ configs: { MEDISPEAK_API_KEY: "********" } });
  expect(await page.evaluate(() => window.careTest.fixtures.plugins.find((p) => p.id === "care_filly")?.configs))
    .toMatchObject({ MEDISPEAK_API_KEY: "sk-test" });
});

test("adds any plugin from a package or git URL", async ({ page }) => {
  await card(page).getByRole("button", { name: "Add plugin…" }).click();
  await dialog(page).getByLabel("Name", { exact: true }).fill("care-abdm");
  await dialog(page).getByLabel("Package", { exact: true }).fill("git+https://github.com/ohcnetwork/care_abdm.git");
  await dialog(page).getByRole("button", { name: "Add and restart CARE" }).click();
  await expect(dialog(page).getByRole("alert")).toContainText("Python app name");
  await dialog(page).getByLabel("Name", { exact: true }).fill("care_abdm");
  await dialog(page).getByLabel("Version", { exact: true }).fill("@v1.0");
  await dialog(page).getByLabel("Settings", { exact: true }).fill('ABDM_ENABLED=true\nABDM_URL=https://abdm.example');
  await dialog(page).getByRole("button", { name: "Add and restart CARE" }).click();
  await expect(dialog(page)).toBeHidden();
  expect(await lastCall(page, "EnablePlugin")).toEqual([{
    id: "care_abdm", package: "git+https://github.com/ohcnetwork/care_abdm.git", version: "@v1.0",
    configs: { ABDM_ENABLED: true, ABDM_URL: "https://abdm.example" },
  }, password]);
  const row = card(page).getByRole("listitem").filter({ hasText: "care_abdm" });
  await expect(row).toContainText("Custom build");
});

test("removes a plugin after confirming", async ({ page }) => {
  await card(page).getByRole("button", { name: "Remove care_multihost_files_be" }).click();
  await dialog(page).getByRole("button", { name: "Remove plugin" }).click();
  await expect(card(page).getByRole("list", { name: "Enabled plugins" })).toContainText("No plugins are enabled.");
  expect(await lastCall(page, "DisablePlugin")).toEqual(["care_multihost_files_be", password]);
});

test("reports a plugin change that failed and was rolled back", async ({ page }) => {
  await page.evaluate(() => { window.careTest.fixtures.finishJobs = false; });
  await card(page).getByRole("button", { name: "Remove care_multihost_files_be" }).click();
  await dialog(page).getByRole("button", { name: "Remove plugin" }).click();
  await page.evaluate(() => window.careTest.finishJob("plugins", "health check failed; rolled back"));
  await expect(page.getByRole("alert").filter({ hasText: "The plugin change didn't finish" }))
    .toContainText("put the previous plugins back");
});

test("plugin model parses settings like the service", () => {
  expect(parseLines('A=1\nB=["x"]\n# note\nC=plain text')).toEqual({ configs: { A: 1, B: ["x"], C: "plain text" }, error: "" });
  expect(parseLines("not a setting").error).toContain("KEY=value");
  const draft = draftFor(undefined, { id: "care_x", package: "care-x", configs: { TOKEN: "********", N: 2 } });
  expect(draft.extra).toBe("TOKEN=********\nN=2");
  expect(pluginFrom(draft).plugin).toEqual({ id: "care_x", package: "care-x", version: "", configs: { TOKEN: "********", N: 2 } });
});
