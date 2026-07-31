import { expect, test, type APIRequestContext } from "@playwright/test";

const screenshotDir =
  process.env.SCHEDULE_BUILDER_SCREENSHOT_DIR ??
  "../docs/screenshots/schedule-builder-slice3";
const mediaQuery = process.env.SCHEDULE_BUILDER_MEDIA_QUERY ?? "Entourage S01E01";
const readyMediaQuery =
  process.env.SCHEDULE_BUILDER_READY_MEDIA_QUERY ?? "Mythic Quest Ravens Banquet S01E01";

type InventoryItem = {
  mediaId: string;
  title: string;
  profilePackageStatus?: string;
  pendingPackages: number;
  processingPackages: number;
};

type InventoryResponse = {
  media: InventoryItem[];
};

type ProfileListResponse = {
  defaultProfile: string;
  profileDetails: Array<{ name: string; label: string }>;
};

type CreateChannelResponse = {
  channelID: string;
  queued?: string[];
  scheduleEntries: number;
};

test("audit a real on-demand Schedule Builder creation", async ({ page, request }) => {
  test.skip(
    process.env.RUN_SCHEDULE_BUILDER_AUDIT !== "1",
    "This live audit creates and removes a channel; run npm run audit:schedule-builder explicitly.",
  );

  const profileResponse = await request.get("/api/media/package-profiles");
  expect(profileResponse.ok()).toBeTruthy();
  const profiles = await profileResponse.json() as ProfileListResponse;
  const profile = profiles.defaultProfile;
  const profileLabel =
    profiles.profileDetails.find((item) => item.name === profile)?.label ?? profile;

  const candidate = await findMissingCandidate(request, profile);
  const channelName = `Playwright audit ${Date.now()}`;
  let channelID = "";

  try {
    await page.goto("/admin");
    await page.getByRole("button", { name: "Create channel" }).click();
    await expect(page.getByRole("heading", { name: "Create a channel" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Movies" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Shows" })).toBeVisible();
    await expect(
      page.getByText(`On-demand · Back-to-back · ${profileLabel}`),
    ).toBeVisible();
    await page.screenshot({
      path: `${screenshotDir}/01-fresh-channel.png`,
      fullPage: true,
    });

    await page.getByText("Advanced options", { exact: true }).click();
    await expect(page.getByRole("button", { name: "On-demand" })).toHaveClass(/is-active/);
    const profileButton = page.getByRole("button", { name: profileLabel });
    await expect(profileButton).toBeVisible();
    await expect(profileButton).toHaveAttribute("aria-pressed", "true");
    await page.getByText("Advanced options", { exact: true }).click();

    await page.getByRole("button", { name: "Shows" }).click();
    const search = page.getByLabel("Search shows and episodes");
    await expect(search).toBeVisible();
    await expect(search).toBeFocused();
    await page.screenshot({
      path: `${screenshotDir}/02a-show-browser.png`,
      fullPage: true,
    });
    await search.fill(mediaQuery);

    const candidateRow = page.locator("li", { hasText: candidate.title }).first();
    await expect(candidateRow).toBeVisible();
    await page.screenshot({
      path: `${screenshotDir}/02-episode-search.png`,
      fullPage: true,
    });

    await candidateRow.getByRole("button", { name: "Add" }).click();
    await page.getByLabel("Channel name").fill(channelName);
    await expect(
      page.locator(".schedule-timeline-entry-title", { hasText: candidate.title }),
    ).toBeVisible();

    const createButton = page
      .getByRole("region", { name: "Channel summary" })
      .getByRole("button", { name: /Create channel/ });
    await expect(createButton).toBeEnabled();
    await page.screenshot({
      path: `${screenshotDir}/03-populated-draft-desktop.png`,
      fullPage: true,
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => {
      (document.activeElement as HTMLElement | null)?.blur();
      window.scrollTo(0, 0);
      document.querySelector<HTMLElement>(".admin-main")?.scrollTo(0, 0);
      // Isolate the builder at the narrow width. The live appliance's populated
      // sidebar can protrude from its collapsed shell; that shared-shell issue
      // is intentionally outside the Schedule Builder slice.
      const sidebar = document.querySelector<HTMLElement>(".admin-sidebar");
      if (sidebar) sidebar.style.display = "none";
    });
    await page.screenshot({
      path: `${screenshotDir}/04-populated-draft-narrow.png`,
      fullPage: true,
    });
    await page.setViewportSize({ width: 1440, height: 900 });
    await createButton.scrollIntoViewIfNeeded();
    await page.screenshot({
      path: `${screenshotDir}/03b-create-action-desktop.png`,
      fullPage: true,
    });

    const createRequestPromise = page.waitForRequest(
      (candidateRequest) =>
        candidateRequest.method() === "POST" &&
        new URL(candidateRequest.url()).pathname === "/api/channels",
    );
    const createResponsePromise = page.waitForResponse(
      (candidateResponse) =>
        candidateResponse.request().method() === "POST" &&
        new URL(candidateResponse.url()).pathname === "/api/channels",
    );
    await createButton.click();

    const createRequest = await createRequestPromise;
    expect(createRequest.postDataJSON()).toEqual({
      displayName: channelName,
      packageProfile: profile,
      mediaIds: [candidate.mediaId],
      prefillMode: "on_demand",
    });

    const createResponse = await createResponsePromise;
    expect(createResponse.ok()).toBeTruthy();
    const created = await createResponse.json() as CreateChannelResponse;
    channelID = created.channelID;
    expect(channelID).not.toBe("");
    expect(created.scheduleEntries).toBeGreaterThan(0);
    expect(created.queued ?? []).toEqual([]);

    await expect(page.getByRole("heading", { name: "Inventory" })).toBeVisible();
    await page.screenshot({
      path: `${screenshotDir}/05-post-create-handoff.png`,
      fullPage: true,
    });

    const nowResponse = await request.get("/api/now");
    expect(nowResponse.ok()).toBeTruthy();
    const now = await nowResponse.json() as {
      channels: Array<{
        id: string;
        prefillMode?: string;
        packageProfile: string;
        scheduleMode?: string;
      }>;
    };
    expect(now.channels.find((channel) => channel.id === channelID)).toMatchObject({
      prefillMode: "on_demand",
      packageProfile: profile,
      scheduleMode: "back_to_back",
    });

    const after = await getCandidate(request, profile, candidate.mediaId);
    expect(after.profilePackageStatus).toBe("missing");
    expect(after.pendingPackages).toBe(0);
    expect(after.processingPackages).toBe(0);
  } finally {
    if (channelID) {
      await deleteAuditChannel(request, channelID);
    }
  }
});

test("audit a real eager creation with already packaged media", async ({ page, request }) => {
  test.skip(
    process.env.RUN_SCHEDULE_BUILDER_AUDIT !== "1",
    "This live audit creates and removes a channel; run npm run audit:schedule-builder explicitly.",
  );

  const profileResponse = await request.get("/api/media/package-profiles");
  expect(profileResponse.ok()).toBeTruthy();
  const profiles = await profileResponse.json() as ProfileListResponse;
  const profile = profiles.defaultProfile;
  const profileLabel =
    profiles.profileDetails.find((item) => item.name === profile)?.label ?? profile;
  const candidate = await findCandidate(request, profile, readyMediaQuery, "ready");
  const channelName = `Playwright eager audit ${Date.now()}`;
  let channelID = "";

  try {
    await page.goto("/admin");
    await page.getByRole("button", { name: "Create channel" }).click();
    await page.getByText("Advanced options", { exact: true }).click();
    await page.getByRole("button", { name: "Pre-encode" }).click();
    await expect(page.getByRole("button", { name: "Pre-encode" })).toHaveClass(/is-active/);
    await expect(page.getByRole("button", { name: profileLabel })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await page.getByText("Advanced options", { exact: true }).click();

    await page.getByRole("button", { name: "Shows" }).click();
    await page.getByLabel("Search shows and episodes").fill(readyMediaQuery);
    const candidateRow = page.locator("li", { hasText: candidate.title }).first();
    await expect(candidateRow).toBeVisible();
    await candidateRow.getByRole("button", { name: "Add" }).click();
    await page.getByLabel("Channel name").fill(channelName);

    const createButton = page
      .getByRole("region", { name: "Channel summary" })
      .getByRole("button", { name: "Create channel", exact: true });
    await expect(createButton).toBeEnabled();
    await page.screenshot({
      path: `${screenshotDir}/06-ready-eager-draft.png`,
      fullPage: true,
    });

    const createRequestPromise = page.waitForRequest(
      (candidateRequest) =>
        candidateRequest.method() === "POST" &&
        new URL(candidateRequest.url()).pathname === "/api/channels",
    );
    const createResponsePromise = page.waitForResponse(
      (candidateResponse) =>
        candidateResponse.request().method() === "POST" &&
        new URL(candidateResponse.url()).pathname === "/api/channels",
    );
    await createButton.click();

    expect((await createRequestPromise).postDataJSON()).toEqual({
      displayName: channelName,
      packageProfile: profile,
      mediaIds: [candidate.mediaId],
    });
    const createResponse = await createResponsePromise;
    expect(createResponse.ok()).toBeTruthy();
    const created = await createResponse.json() as CreateChannelResponse;
    channelID = created.channelID;
    expect(created.queued ?? []).toEqual([]);

    const nowResponse = await request.get("/api/now");
    expect(nowResponse.ok()).toBeTruthy();
    const now = await nowResponse.json() as {
      channels: Array<{
        id: string;
        prefillMode?: string;
        packageProfile: string;
        scheduleMode?: string;
      }>;
    };
    expect(now.channels.find((channel) => channel.id === channelID)).toMatchObject({
      prefillMode: "eager",
      packageProfile: profile,
      scheduleMode: "back_to_back",
    });

    const after = await findCandidate(request, profile, readyMediaQuery, "ready");
    expect(after.mediaId).toBe(candidate.mediaId);
    expect(after.pendingPackages).toBe(0);
    expect(after.processingPackages).toBe(0);
  } finally {
    if (channelID) {
      await deleteAuditChannel(request, channelID);
    }
  }
});

async function findMissingCandidate(
  request: APIRequestContext,
  profile: string,
): Promise<InventoryItem> {
  const response = await request.get("/api/media/inventory", {
    params: {
      q: mediaQuery,
      profile,
      kind: "programs",
      codecStatus: "passed",
      limit: 100,
    },
  });
  expect(response.ok()).toBeTruthy();
  const body = await response.json() as InventoryResponse;
  const candidate = body.media.find(
    (item) =>
      item.profilePackageStatus === "missing" &&
      item.pendingPackages === 0 &&
      item.processingPackages === 0,
  );
  expect(
    candidate,
    `query "${mediaQuery}" must return an unpackaged idle media item`,
  ).toBeTruthy();
  return candidate!;
}

async function findCandidate(
  request: APIRequestContext,
  profile: string,
  query: string,
  packageStatus: string,
): Promise<InventoryItem> {
  const response = await request.get("/api/media/inventory", {
    params: {
      q: query,
      profile,
      profilePackageStatus: packageStatus,
      kind: "programs",
      codecStatus: "passed",
      limit: 100,
    },
  });
  expect(response.ok()).toBeTruthy();
  const body = await response.json() as InventoryResponse;
  const candidate = body.media.find(
    (item) =>
      item.profilePackageStatus === packageStatus &&
      item.pendingPackages === 0 &&
      item.processingPackages === 0,
  );
  expect(
    candidate,
    `query "${query}" must return an idle ${packageStatus} media item`,
  ).toBeTruthy();
  return candidate!;
}

async function getCandidate(
  request: APIRequestContext,
  profile: string,
  mediaId: string,
): Promise<InventoryItem> {
  const response = await request.get("/api/media/inventory", {
    params: { q: mediaQuery, profile, limit: 100 },
  });
  expect(response.ok()).toBeTruthy();
  const body = await response.json() as InventoryResponse;
  const candidate = body.media.find((item) => item.mediaId === mediaId);
  expect(candidate, `media ${mediaId} must remain in inventory`).toBeTruthy();
  return candidate!;
}

async function deleteAuditChannel(
  request: APIRequestContext,
  channelID: string,
) {
  const disableResponse = await request.patch(
    `/api/channels/${encodeURIComponent(channelID)}`,
    { data: { enabled: false } },
  );
  expect(disableResponse.ok()).toBeTruthy();
  const deleteResponse = await request.delete(
    `/api/channels/${encodeURIComponent(channelID)}`,
  );
  expect(deleteResponse.ok()).toBeTruthy();

  const nowResponse = await request.get("/api/now");
  const now = await nowResponse.json() as { channels: Array<{ id: string }> };
  expect(now.channels.some((channel) => channel.id === channelID)).toBeFalsy();
}
