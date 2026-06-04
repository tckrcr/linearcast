export type MediaWindow = {
  mediaID: string;
  title?: string;
  path?: string;
  schedulingGroup?: string;
  packageStatus?: string;
  packageError?: string;
  startMs: number;
  endMs: number;
  durationMs: number;
  elapsedMs?: number;
  remainingMs?: number;
};

export type CacheStatus = {
  format?: string;
  hasSchedule: boolean;
  cacheSize: number;
  cacheMinIndex?: number;
  cacheMaxIndex?: number;
  lookaheadDepthSegments?: number;
  lookaheadDepthSeconds?: number;
  latestGeneratedIndex?: number;
  latestGeneratedSeconds?: number;
  latestGeneratedAt?: string;
};

export type ChannelStatus = "playing" | "gap" | "unscheduled" | string;

export type NowPlaying = {
  title?: string;
  artist?: string;
  album?: string;
  artUrl?: string;
  playing: boolean;
};

export type ChannelNow = {
  id: string;
  displayName: string;
  artworkUrl?: string;
  enabled: boolean;
  hiddenFromGuide: boolean;
  ordering: string;
  mediaKind: "video" | "music" | string;
  status: ChannelStatus;
  current: MediaWindow | null;
  next: MediaWindow | null;
  scheduleCoverageMs: number;
  scheduleCoverageHours: number;
  scheduleEndMs?: number;
  packageCoverageMs: number;
  packageCoverageHours: number;
  packageReadyCount: number;
  packageProfile: string;
  isExternal?: boolean;
  upstreamHlsUrl?: string;
  nowPlaying?: NowPlaying;
  cache?: CacheStatus;
};

export type AdminNow = {
  nowMs: number;
  channels: ChannelNow[];
};

export type PlayableSource = {
  id: string;
  displayName: string;
  artworkUrl?: string;
  kind: "vod" | "live" | string;
  playbackType: "hls" | string;
  status: ChannelStatus;
  manifestUrl: string;
  enabled: boolean;
  current?: MediaWindow | null;
  next?: MediaWindow | null;
  scheduleCoverageMs?: number;
  scheduleCoverageHours?: number;
  packageCoverageMs?: number;
  packageCoverageHours?: number;
  packageProfile?: string;
  nowPlaying?: NowPlaying;
};

export type PlayableSourcesResponse = {
  nowMs: number;
  sources: PlayableSource[];
  generatedAt: string;
};

// GuideEntry is the viewer-safe schedule entry returned by GET /api/guide.
// It deliberately omits filesystem path / scheduling-group detail.
export type GuideEntry = {
  entryId: string;
  mediaId: string;
  title?: string;
  startMs: number;
  endMs: number;
  durationMs: number;
};

export type GuideChannel = {
  id: string;
  displayName: string;
  artworkUrl?: string;
  status: ChannelStatus;
  isExternal?: boolean;
  nowPlaying?: NowPlaying;
  // End of the channel's last scheduled entry; used to stop paging the guide
  // past where a schedule has actually been built.
  scheduleEndMs?: number;
  entries: GuideEntry[];
};

export type GuideResponse = {
  nowMs: number;
  fromMs: number;
  toMs: number;
  channels: GuideChannel[];
};

export type ChannelSummary = {
  id: string;
  displayName: string;
  artworkUrl?: string;
  enabled: boolean;
  hiddenFromGuide: boolean;
  mediaKind: "video" | "music" | string;
};

export type ChannelCloneResponse = {
  sourceChannelID: string;
  channelID: string;
  displayName: string;
  enabled: boolean;
  mediaCount: number;
};

export type SubtitleSettings = {
  subtitleAutoEnable: boolean;
  subtitleLanguagePreference: string[];
};

export type SchedulerTunables = {
  horizonHours: number;
  lowWaterHours: number;
  tickSeconds: number;
};

export type EncoderSweeperSettings = {
  sweepIntervalSeconds: number;
  maxAttempts: number;
};

export type ChannelPolicy = {
  channelId: string;
  playbackMode: string;
  requiredPackageProfile: string;
  packagePrefillMs: number | null;
  mediaKind: "video" | "music" | string;
};

export type InvalidProfilePackageCleanupResponse = {
  dryRun: boolean;
  removed: Array<{
    id: string;
    mediaId: string;
    profile: string;
    status: string;
    packageRoot?: string;
    bytes?: number;
    diskSkipped?: boolean;
  }>;
  totalBytes: number;
};

export type CacheSummary = {
  generatedAt: string;
  cacheRoot?: string;
  packageRoot?: string;
  cacheRootBytes?: number;
  packageRootBytes?: number;
  packageBytes?: number;
  packageRootCount: number;
  encoderCount: number;
  statusCounts: Array<{
    status: string;
    count: number;
  }>;
  packageSummaries: Array<{
    renditionProfile: string;
    status: string;
    packageCount: number;
    packageBytes: number;
    readyDurationMs: number;
    oldestUpdatedMs?: number;
    newestUpdatedMs?: number;
    invalid?: boolean;
    disabled?: boolean;
  }>;
  channelSummaries: Array<{
    channelId: string;
    displayName: string;
    renditionProfile: string;
    status: string;
    packageCount: number;
    packageBytes: number;
    readyDurationMs: number;
    oldestUpdatedMs?: number;
    newestUpdatedMs?: number;
  }>;
  channelNeeds: Array<{
    channelId: string;
    displayName: string;
    renditionProfile: string;
    neededCount: number;
    readyCount: number;
    processingCount: number;
    pendingCount: number;
    failedCount: number;
    missingCount: number;
    remainingCount: number;
  }>;
  warnings?: string[];
};

export type MissingMediaMaintenanceResponse = {
  generatedAt: string;
  dryRun: boolean;
  checked: number;
  missing: Array<{
    id: string;
    path: string;
    title?: string;
  }>;
  errors?: Array<{
    id: string;
    path: string;
    error: string;
  }>;
  deleted: number;
};

export type OrphanPackagesMaintenanceResponse = {
  generatedAt: string;
  dryRun: boolean;
  packageRoot?: string;
  unreferenced: Array<{
    id: string;
    mediaId: string;
    renditionProfile: string;
    status: string;
    packageRoot?: string;
    bytes?: number;
    diskSkipped?: boolean;
  }>;
  orphanDirs: Array<{
    path: string;
    bytes?: number;
  }>;
  totalBytes: number;
  deletedRows: number;
  deletedDirs: number;
  warnings?: string[];
};

export type OptimizeDBMaintenanceResponse = {
  generatedAt: string;
  durationMs: number;
  sizeBefore: number;
  sizeAfter: number;
};

export type PlexStatus = {
  connected: boolean;
  username?: string;
  serverName?: string;
  url?: string;
  pathMap?: string;
};

export type JellyfinStatus = {
  connected: boolean;
  configured: boolean;
  url?: string;
  serverName?: string;
  version?: string;
  pathMap?: string;
};

export type LocalMediaSource = {
  id: string;
  name: string;
  mediaKind: "movies" | "shows" | "music";
  paths: string[];
  createdAtMs: number;
  updatedAtMs: number;
};

export type ScheduleEntry = {
  entryId: string;
  mediaId: string;
  title?: string;
  path?: string;
  schedulingGroup?: string;
  startMs: number;
  endMs: number;
  offsetMs?: number;
  durationMs: number;
};

export type ChannelMedia = {
  mediaId: string;
  title?: string;
  path?: string;
  schedulingGroup?: string;
  durationMs: number;
  codecCheckPassed: boolean;
  codecCheckReason?: string;
  packageId?: string;
  packageStatus: string;
  packageReady: boolean;
  packagedDurationMs?: number;
  packageError?: string;
};

export type ChannelMediaList = {
  channelId: string;
  displayName: string;
  requiredPackageProfile: string;
  count: number;
  media: ChannelMedia[];
};

export type FillerAsset = {
  id: string;
  mediaId: string;
  label: string;
  kind: "filler" | "bumper" | "station_id" | string;
  enabled: boolean;
  createdAtMs: number;
};

export type ChannelFillerAsset = FillerAsset & {
  channelId: string;
  weight: number;
  channelEnabled: boolean;
  path: string;
  title?: string;
  schedulingGroup?: string;
  durationMs: number;
  packageId?: string;
  packageStatus: string;
  packageReady: boolean;
  packagedDurationMs?: number;
  packageError?: string;
};

export type ChannelFillerAssetList = {
  channelId: string;
  requiredPackageProfile: string;
  count: number;
  assets: ChannelFillerAsset[];
};

export type MediaPackageCandidate = {
  mediaId: string;
  title?: string;
  path: string;
  schedulingGroup?: string;
  durationMs: number;
  packageId?: string;
  packageStatus: "missing" | "pending" | "processing" | "failed" | string;
  packageProfile?: string;
  packageError?: string;
  packagedDurationMs?: number;
  updatedAtMs?: number;
  selectable: boolean;
};

export type MediaPackageCandidateList = {
  profile: string;
  count: number;
  statusCounts: Array<{
    status: string;
    count: number;
  }>;
  media: MediaPackageCandidate[];
};

export type PackageProfile = {
  name: string;
  label: string;
  description: string;
  mediaKind?: "video" | "music" | string;
  isBuiltin?: boolean;
  disabled?: boolean;
  references?: {
    mediaPackages: number;
    channels: number;
    scheduleEntries: number;
  };
  video: {
    mode: string;
    codec?: string;
    codecRequired?: string;
    profile?: string;
    preset?: string;
    crf?: number;
    scaleHeight?: number;
    videoBitrate?: string;
    videoMaxBitrate?: string;
    videoQuality?: number;
  };
  audio: {
    mode: string;
    codec?: string;
    bitrate?: string;
    channels?: number;
    sampleHz?: number;
  };
};

export type MediaPackageRequestResult = {
  profile: string;
  queued: string[];
  alreadyPending: string[];
  alreadyReady: string[];
  failed: Array<{ mediaId: string; code: string; message: string }>;
};

export type MediaPackageCancelResult = {
  profile: string;
  canceledPending: number;
  canceledProcessing: number;
  skippedReady: number;
  skippedFailed: number;
  skippedMissing: number;
  affectedMediaIds: string[];
};

export type EncoderCurrentJob = {
  packageId: string;
  mediaId: string;
  mediaTitle: string;
  profile: string;
  progressPct?: number;
  leaseExpiresMs: number;
  claimedAtMs: number;
};

export type EncoderListItem = {
  id: string;
  name: string;
  capabilities?: unknown;
  status: "pending" | "online" | "offline" | "draining" | string;
  lastSeenMs: number;
  createdAtMs: number;
  revokedAtMs?: number;
  concurrency: number;
  jobs?: EncoderCurrentJob[];
};

export type LocalWorkerItem = {
  id: "local";
  name: string;
  capabilities?: unknown;
  status: "pending" | "online" | "offline" | "draining" | string;
  lastSeenMs: number;
  createdAtMs: number;
  enabled: boolean;
  concurrency: number;
  jobs?: EncoderCurrentJob[];
};

export type EncoderListResponse = {
  encoders: EncoderListItem[];
  localWorker: LocalWorkerItem;
};

export type EncoderRegisterResponse = {
  id: string;
  name: string;
  apiKey: string;
  createdAtMs: number;
};

export type EncoderDownloadEntry = {
  platform: string;
  label: string;
  filename: string;
  os: "darwin" | "windows" | "linux" | string;
};

export type EncoderDownloadsResponse = {
  available: EncoderDownloadEntry[];
  distConfigured: boolean;
};

export type ChannelSchedule = {
  channelId: string;
  displayName: string;
  fromMs: number;
  toMs: number;
  count: number;
  entries: ScheduleEntry[];
};

export type SchedulePreviewWarning = {
  code: string;
  message: string;
};

export type SchedulePreviewDiff = {
  unchanged: number;
  added: ScheduleEntry[];
  removed: ScheduleEntry[];
};

export type ChannelSchedulePreview = {
  channelId: string;
  displayName: string;
  ordering: string;
  profile: string;
  fromMs: number;
  toMs: number;
  generatedEndMs: number;
  count: number;
  eligibleMedia: number;
  eligibleReadyMedia: number;
  warnings: SchedulePreviewWarning[];
  entries: ScheduleEntry[];
  diff: SchedulePreviewDiff;
};

export type PlaybackStats = {
  readyState: number;
  paused: boolean;
  currentTime: number;
  playbackRate: number;
  droppedFrames: number;
  totalFrames: number;
  bufferAhead: number;
  buffered: string;
  hlsLatency: number | null;
  liveSyncPosition: number | null;
  lastFrag: string;
  lastEvent: string;
  playbackEngine: string;
  errors: string[];
  streamUnavailable: boolean;
  streamUnavailableReason?: string;
};

export type StreamProbe = {
  status: "checking" | "ready" | "waiting";
  detail: string;
};

export type AdminWriteLogEntry = {
  id: number;
  createdAtMs: number;
  method: string;
  path: string;
  action: string | null;
  targetType: string | null;
  targetId: string | null;
  status: number;
  durationMs: number;
};

export type MediaLibrary = {
  id?: string;   // Jellyfin
  key?: string;  // Plex
  name?: string; // Jellyfin
  title?: string; // Plex
  type: string;
};

export type PolicyDraft = { profile: string; prefillHours: string; mediaKind: "video" | "music"; loaded: boolean };

export type ProfileReadiness = {
  channelId: string;
  profile: string;
  total: number;
  ready: number;
  pending: number;
  processing: number;
  failed: number;
  missing: number;
};
export type RowBusy = Record<string, boolean>;
export type RowStatus = Record<string, string>;
export type ScheduleEditTarget =
  | { mode: "jump"; startMs: number; label: string }
  | { mode: "fill"; startMs: number; label: string }
  | {
      mode: "choose";
      entryId: string;
      startMs: number;
      endMs: number;
      label: string;
      canInsertAfter: boolean;
      canInsertBefore: boolean;
      isCurrentlyPlaying: boolean;
    }
  | { mode: "insert-after"; afterEntryId: string; label: string }
  | { mode: "insert-before"; beforeEntryId: string; label: string };
export type DraftChannelConfig = {
  packageProfile: string;
  displayName: string;
  onImported: (channelId: string) => void;
};

export type ScheduleInsertItem = {
  mediaId: string;
  title?: string;
  path?: string;
  schedulingGroup?: string;
  durationMs: number;
  packagedDurationMs?: number;
  packageReady: boolean;
  channelMember?: boolean;
  fillerAsset?: boolean;
};

export type ScheduleDraftEntry = ChannelSchedule["entries"][number] & {
  draftId: string;
  needsPackage?: boolean;
};
