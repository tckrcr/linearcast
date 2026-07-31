import { expect, test } from "@playwright/test";

test("viewer keeps tuning state until channel metadata resolves", async ({
  page,
}) => {
  let releaseSources!: () => void;
  const sourcesHeld = new Promise<void>((resolve) => {
    releaseSources = resolve;
  });
  const requestedPaths: string[] = [];
  page.on("request", (request) => {
    requestedPaths.push(new URL(request.url()).pathname);
  });
  await page.route("**/api/playable-sources", async (route) => {
    await sourcesHeld;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        nowMs: Date.now(),
        sources: [],
        generatedAt: new Date().toISOString(),
      }),
    });
  });

  try {
    await page.goto("/");
    await expect(page.getByText("Tuning in…", { exact: true })).toBeVisible();
    await expect(
      page.getByText("No channels configured", { exact: true }),
    ).toHaveCount(0);
    expect(requestedPaths).not.toContain("/__no_source__");
  } finally {
    releaseSources();
  }

  await expect(
    page.getByText("No channels configured", { exact: true }),
  ).toBeVisible();
});
