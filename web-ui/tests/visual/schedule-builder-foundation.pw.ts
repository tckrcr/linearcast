import { expect, test, type Route } from "@playwright/test";
import {
  fixtureEpisode,
  fixtureProfile,
  stubScheduleBuilderReads,
} from "./schedule-builder-fixture";

test("fresh channel creation puts name and content before advanced policy", async ({ page }) => {
  let createRequest: Record<string, unknown> | null = null;
  await stubScheduleBuilderReads(page, async (route: Route) => {
    if (route.request().method() === "POST") {
      createRequest = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        json: {
          channelID: "fixture-channel",
          queued: [],
          scheduleEntries: 49,
        },
      });
      return;
    }
    await route.fulfill({ json: { channels: [] } });
  });

  await page.goto("/admin");
  await page.getByRole("button", { name: "Create channel" }).click();

  await expect(page.getByRole("heading", { name: "Create a channel" })).toBeVisible();
  await expect(page.getByLabel("Channel name")).toBeVisible();
  await expect(page.getByRole("button", { name: "Movies" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Shows" })).toBeVisible();
  await expect(page.getByRole("button", { name: "On-demand" })).toBeHidden();
  await expect(page.getByRole("button", { name: "Back-to-back" })).toBeHidden();
  await expect(
    page.getByText("On-demand · Back-to-back · Broad compatibility"),
  ).toBeVisible();

  await page.getByText("Advanced options", { exact: true }).click();
  await expect(page.getByRole("button", { name: "On-demand" })).toHaveClass(/is-active/);
  await expect(page.getByRole("button", { name: "Pre-encode" })).toBeVisible();
  await expect(page.getByRole("button", { name: fixtureProfile.label })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(page.getByRole("button", { name: "Back-to-back" })).toBeHidden();

  await page.getByRole("button", { name: "Shows" }).click();
  await page.getByLabel("Search shows and episodes").fill("S01E01");
  await page
    .locator("li", { hasText: fixtureEpisode.title })
    .getByRole("button", { name: "Add" })
    .click();
  await page.getByLabel("Channel name").fill("Fixture Channel");

  await expect(page.getByText("Align programs to a time grid")).toBeVisible();
  const createButton = page
    .getByRole("region", { name: "Channel summary" })
    .getByRole("button", { name: "Create channel", exact: true });
  await expect(createButton).toBeEnabled();

  await page.setViewportSize({ width: 390, height: 844 });
  const panelBox = await page.locator(".sb-panel").boundingBox();
  expect(panelBox).not.toBeNull();
  expect(panelBox!.x).toBeGreaterThanOrEqual(0);
  expect(panelBox!.x + panelBox!.width).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1440, height: 900 });

  await createButton.click();

  await expect.poll(() => createRequest).toEqual({
    displayName: "Fixture Channel",
    packageProfile: fixtureProfile.name,
    mediaIds: [fixtureEpisode.mediaId],
    prefillMode: "on_demand",
  });
});
