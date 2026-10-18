import { expect, type Page } from "@playwright/test";
import type {} from "../fixtures/host";

const next = (page: Page) => page.getByRole("button", { name: "Continue", exact: true });

export async function continueServerChecks(page: Page) {
  await expect(page.locator("#setup-title")).toHaveText("Checking this computer");
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Choosing the clinic address");
}

export async function completeBackups(page: Page) {
  await expect(page.locator("#setup-title")).toHaveText("Setting up backups");
  await page.getByRole("button", { name: "Choose where to save", exact: true }).click();
  await page.getByRole("button", { name: "Select saved file", exact: true }).click();
  await expect(page.getByText("Recovery file checked", { exact: true })).toBeVisible();
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Creating the admin password");
}

export async function reachReview(page: Page, { address = "care-hospital", password = "ClinicTest123" } = {}) {
  await continueServerChecks(page);
  await page.getByLabel("Clinic address", { exact: true }).fill(address);
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await completeBackups(page);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await expect(next(page)).toBeEnabled();
  await next(page).click();
  await expect(page.locator("#setup-title")).toHaveText("Review before installing");
}
