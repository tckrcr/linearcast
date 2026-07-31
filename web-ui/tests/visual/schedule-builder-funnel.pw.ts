import { expect, test, type Route } from "@playwright/test";
import { fixtureEpisode, stubScheduleBuilderReads } from "./schedule-builder-fixture";

test("the creation funnel preserves a fresh draft across admin navigation", async ({ page }) => {
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

  const nameInput = page.getByLabel("Channel name");
  await expect(nameInput).toBeFocused();
  const progress = page.getByRole("list", { name: "Channel creation progress" });
  await expect(progress.getByRole("listitem")).toContainText([
    "Name",
    "Content",
    "Review",
  ]);

  await nameInput.fill("Preserved Fixture");
  await page.getByRole("button", { name: "Shows" }).click();
  await page.getByLabel("Search shows and episodes").fill("S01E01");
  await page
    .locator("li", { hasText: fixtureEpisode.title })
    .getByRole("button", { name: "Add" })
    .click();

  const summary = page.getByRole("region", { name: "Channel summary" });
  await expect(summary).toContainText("Preserved Fixture");
  await expect(summary).toContainText("1 program");
  await expect(summary).toContainText("On-demand");

  await page.getByRole("button", { name: "Guide" }).click();
  await page.getByRole("button", { name: "Create channel" }).click();
  await expect(nameInput).toHaveValue("Preserved Fixture");
  await expect(
    page.locator(".schedule-timeline-entry-title", { hasText: fixtureEpisode.title }),
  ).toBeVisible();

  await summary.getByRole("button", { name: "Create channel", exact: true }).click();
  await expect.poll(() => createRequest).not.toBeNull();
  await page.getByRole("button", { name: "Create channel" }).click();
  await expect(page.getByLabel("Channel name")).toHaveValue("");
  await expect(page.getByRole("region", { name: "Channel summary" })).toBeHidden();
});
