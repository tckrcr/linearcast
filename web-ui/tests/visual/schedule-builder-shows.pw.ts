import { expect, test } from "@playwright/test";
import {
  fixtureEpisode,
  fixtureEpisode2,
  stubScheduleBuilderReads,
} from "./schedule-builder-fixture";

test("Shows opens as a populated browser and separates show and episode matches", async ({ page }) => {
  await stubScheduleBuilderReads(page);

  await page.goto("/admin");
  await page.getByRole("button", { name: "Create channel" }).click();
  await page.getByRole("button", { name: "Shows" }).click();

  const search = page.getByLabel("Search shows and episodes");
  await expect(search).toBeFocused();
  await expect(page.getByRole("button", { name: "Open Fixture Show" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open Another Show" })).toBeVisible();

  await search.fill("Fixture");
  await expect(page.getByRole("button", { name: "Open Fixture Show" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open Another Show" })).toBeHidden();
  await expect(page.getByRole("heading", { name: "Individual episodes" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add" })).toBeHidden();

  await search.fill("Second");
  await expect(page.locator("li", { hasText: fixtureEpisode2.title })).toBeVisible();

  await search.fill("S01E02");
  const episodeRow = page.locator("li", { hasText: fixtureEpisode2.title });
  await expect(episodeRow).toBeVisible();
  await episodeRow.getByRole("button", { name: "Add" }).click();
  await expect(page.getByLabel("Channel name")).toHaveValue("Fixture Show");
  await expect(page.getByRole("region", { name: "Channel summary" })).toContainText("1 program");

  await search.clear();
  await expect(page.getByRole("button", { name: "Open Fixture Show" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Open Another Show" })).toBeVisible();

  await page.getByRole("button", { name: "Open Fixture Show" }).click();
  await expect(page.getByRole("heading", { name: "Fixture Show" })).toBeVisible();
  await page.getByRole("button", { name: "Expand Season 1" }).click();
  const seasonEpisodes = page.getByRole("list", { name: "Season 1 episodes" });
  await expect(seasonEpisodes.locator("li", { hasText: fixtureEpisode.title })).toBeVisible();
  await expect(
    seasonEpisodes.locator("li", { hasText: fixtureEpisode2.title }).getByRole("button", { name: "Added" }),
  ).toBeDisabled();
  await seasonEpisodes
    .locator("li", { hasText: fixtureEpisode.title })
    .getByRole("button", { name: "Add" })
    .click();
  await expect(page.getByRole("region", { name: "Channel summary" })).toContainText("2 programs");

  await expect(page.getByRole("button", { name: "Add season" })).toBeVisible();
  await page.getByRole("button", { name: "Add all" }).click();
  await expect(page.getByRole("region", { name: "Channel summary" })).toContainText("2 programs");
});
