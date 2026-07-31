import { expect, test } from "@playwright/test";

// The .pw.ts suffix keeps this live-browser capture out of the Vitest suite.
const screenshotDir =
  process.env.VIEWER_SCREENSHOT_DIR ?? "../docs/screenshots/viewer-before";

test("capture the current viewer and guide", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".tv-stage")).toBeVisible();
  await expect(page.getByRole("button", { name: "Channels" })).toBeVisible();

  // Preserve the first-visit keyboard hint as its own state.
  await page.mouse.move(720, 450);
  await expect(page.locator(".tv-hint")).toHaveClass(/is-visible/);
  await page.screenshot({
    path: `${screenshotDir}/01-first-visit.png`,
    fullPage: true,
  });

  // Preserve the normal tune/warm-up presentation once channel metadata lands.
  await expect(page.locator(".tv-banner")).toHaveClass(/is-visible/);
  await page.screenshot({
    path: `${screenshotDir}/02-tuning.png`,
    fullPage: true,
  });

  // Select an eager channel whose current package is ready so the baseline
  // also covers actual playback chrome without depending on one channel ID.
  const readyChannelID = await page.evaluate(async () => {
    const response = await fetch("/api/playable-sources", { cache: "no-store" });
    const body = await response.json() as {
      sources: Array<{
        id: string;
        prefillMode?: string;
        current?: { packageStatus?: string } | null;
      }>;
    };
    return body.sources.find(
      (source) =>
        source.prefillMode === "eager" &&
        source.current?.packageStatus === "ready",
    )?.id ?? "";
  });
  expect(readyChannelID, "a ready eager channel is required for playback capture").not.toBe("");
  await page.evaluate(() => window.localStorage.setItem("tc.hintSeen", "1"));
  await page.evaluate(
    (channelID) => window.localStorage.setItem("tc.activeChannelId", channelID),
    readyChannelID,
  );
  await page.reload();
  await expect(page.locator(".tv-stage")).toBeVisible();
  await expect(page.locator(".tv-controls")).toBeAttached({ timeout: 45_000 });
  await page.mouse.move(720, 450);
  await expect(page.locator(".tv-banner")).toHaveClass(/is-visible/);
  await expect(page.locator(".tv-controls")).toHaveClass(/is-visible/);
  await page.screenshot({
    path: `${screenshotDir}/03-playback-chrome.png`,
    fullPage: true,
  });

  await page.getByRole("button", { name: "Channels" }).click();
  await expect(page.locator(".tv-channels-overlay")).toBeVisible();
  await expect(page.locator(".guide-grid")).toBeVisible();
  await page.screenshot({
    path: `${screenshotDir}/04-guide.png`,
    fullPage: true,
  });
});
