import type { Page, Route } from "@playwright/test";

export const fixtureProfile = {
  name: "fixture-profile",
  label: "Broad compatibility",
  description: "Fixture profile",
  mediaKind: "video",
  video: { mode: "transcode" },
  audio: { mode: "transcode" },
  subtitles: {},
  isBuiltin: true,
  disabled: false,
  references: {
    mediaPackages: 0,
    channels: 0,
    scheduleEntries: 0,
  },
};

export const fixtureEpisode = {
  mediaId: "fixture-s01e01",
  title: "Fixture Show S01E01 — Pilot",
  path: "/media/Fixture.Show.S01E01.mkv",
  pathRoot: "/media",
  episodeCode: "S01E01",
  seasonNumber: 1,
  episodeNumber: 1,
  collection: "Fixture Show",
  source: "local",
  mediaKind: "video",
  durationMs: 1_800_000,
  container: "mkv",
  videoCodec: "h264",
  audioCodec: "aac",
  codecCheckPassed: true,
  profilePackageStatus: "missing",
  readyPackages: 0,
  pendingPackages: 0,
  processingPackages: 0,
  failedPackages: 0,
};

export const fixtureEpisode2 = {
  ...fixtureEpisode,
  mediaId: "fixture-s01e02",
  title: "Fixture Show S01E02 — Second",
  path: "/media/Fixture.Show.S01E02.mkv",
  episodeCode: "S01E02",
  episodeNumber: 2,
};

export const fixtureShows = [
  {
    collectionId: "fixture-show",
    name: "Fixture Show",
    episodeCount: 2,
    durationMs: 3_600_000,
    seasonCount: 1,
    seasons: [{ seasonNumber: 1, episodeCount: 2, durationMs: 3_600_000 }],
  },
  {
    collectionId: "another-show",
    name: "Another Show",
    episodeCount: 8,
    durationMs: 14_400_000,
    seasonCount: 2,
    seasons: [
      { seasonNumber: 1, episodeCount: 4, durationMs: 7_200_000 },
      { seasonNumber: 2, episodeCount: 4, durationMs: 7_200_000 },
    ],
  },
];

export async function stubScheduleBuilderReads(
  page: Page,
  onChannels?: (route: Route) => Promise<void> | void,
) {
  await page.route("**/api/auth/status", (route) =>
    route.fulfill({
      json: { authenticated: true, enabled: false, mustChange: false },
    }),
  );
  await page.route("**/api/now", (route) =>
    route.fulfill({ json: { nowMs: Date.now(), channels: [] } }),
  );
  await page.route("**/api/channels", async (route) => {
    if (onChannels) {
      await onChannels(route);
      return;
    }
    await route.fulfill({ json: { channels: [] } });
  });
  await page.route("**/api/admin/media-sources/status", (route) =>
    route.fulfill({
      json: {
        hasMediaSource: true,
        plexConfigured: false,
        jellyfinConfigured: false,
        localSourceCount: 1,
      },
    }),
  );
  await page.route("**/api/media/package-profiles", (route) =>
    route.fulfill({
      json: {
        defaultProfile: fixtureProfile.name,
        profiles: [fixtureProfile.name],
        profileDetails: [fixtureProfile],
      },
    }),
  );
  await page.route("**/api/media/shows", (route) =>
    route.fulfill({ json: { shows: fixtureShows } }),
  );
  await page.route("**/api/media/by-group?*", (route) =>
    route.fulfill({
      json: [fixtureEpisode, fixtureEpisode2].map((episode) => ({
        mediaId: episode.mediaId,
        title: episode.title,
        path: episode.path,
        collectionName: episode.collection,
        durationMs: episode.durationMs,
        codecCheckPassed: true,
      })),
    }),
  );
  await page.route("**/api/media/inventory?*", (route) => {
    const url = new URL(route.request().url());
    const query = (url.searchParams.get("q") ?? "").toLowerCase();
    const media = url.searchParams.has("collection")
      ? [fixtureEpisode, fixtureEpisode2]
      : query.includes("s01e02") || query.includes("second")
        ? [fixtureEpisode2]
        : query.includes("s01e01") || query.includes("pilot")
          ? [fixtureEpisode]
          : [fixtureEpisode, fixtureEpisode2];
    return route.fulfill({
      json: {
        count: media.length,
        limit: 100,
        offset: 0,
        media,
      },
    });
  });
}
