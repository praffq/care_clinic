import { expect, type Page } from "@playwright/test";
import type {} from "../fixtures/host";

const next = (page: Page) => page.getByRole("button", { name: "Continue", exact: true });

export async function continueServerChecks(page: Page) {
  await expect(page.locator("#setup-title")).toHaveText("Checking this computer");
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Removing stale files from an earlier setup");
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Choosing the clinic address");
}

export async function saveRecoveryCodes(page: Page) {
  await page.getByRole("button", { name: "Choose where to save", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Recovery codes saved", exact: true })).toBeVisible();
}

export async function reachReview(page: Page, { address = "care-hospital", password = "ClinicTest123" } = {}) {
  await continueServerChecks(page);
  await page.getByLabel("Clinic address", { exact: true }).fill(address);
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Creating the admin password");
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await saveRecoveryCodes(page);
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Review before installing");
}
