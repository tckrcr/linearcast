export type MediaWindow = {
  mediaID: string;
  title?: string;
  path?: string;
  collectionName?: string;
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

// "live" / "down" are reported for external (live-proxy) channels by the admin
// reachability heartbeat; "unknown" before the first probe resolves.
export type ChannelStatus = "playing" | "gap" | "unscheduled" | "live" | "down" | "unknown" | string;

export type NowPlaying = {
  title?: string;
  artist?: string;
  album?: string;
  artUrl?: string;
  playing: boolean;
};

// SpotifyUrl is the singleton Spotify→HLS URL. configured is false when none is
// set.
export type SpotifyUrl = {
  configured: boolean;
  channelId?: string;
  displayName?: string;
  upstreamHlsUrl?: string;
  status?: ChannelStatus;
  nowPlaying?: NowPlaying;
};

export type ChannelNow = {
  id: string;
  displayName: string;
  artworkUrl?: string;
  enabled: boolean;
  hiddenFromGuide: boolean;
  ordering: string;
  mediaKind: "video" | "music" | string;
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
  prefillMode?: "eager" | "on_demand" | string;
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
  playbackMode?: string;
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
  adaptiveBitrate?: boolean;
  prefillMode?: "eager" | "on_demand" | string;
  playbackMode?: string;
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
  prefillMode?: "eager" | "on_demand" | string;
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
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
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
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

export type PublicServerURL = {
  publicServerUrl: string;
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
  adaptiveBitrate: boolean;
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

export type ImportPackagesResponse = {
  generatedAt: string;
  scanned: number;
  imported: Array<{
    mediaId: string;
    profile: string;
    packageId: string;
    segmentCount: number;
    durationMs: number;
  }>;
  alreadyReady: number;
  needsMedia: string[];
  skipped: Array<{
    path: string;
    reason: string;
  }>;
};

export type PackageIntegrityItem = {
  packageId: string;
  mediaId: string;
  profile: string;
  status: string;
  checked: boolean;
  initPresent: boolean;
  manifestPresent: boolean;
  segmentCount: number;
  missingSegments?: string[];
  fileError?: string;
  packagedMs?: number;
  sourceMs?: number;
  shortfallMs?: number;
  durationUnknown?: boolean;
  truncated?: boolean;
  ok: boolean;
};

export type PackageIntegrityResponse = {
  generatedAt: string;
  mediaId?: string;
  checked: number;
  problems: number;
  unknownDuration: number;
  packages: PackageIntegrityItem[];
};

export type PackageIntegrityRepairResponse = {
  generatedAt: string;
  fileReset: number;
  durationReset: number;
  durationSkipped: number;
};

export type PackageRequeueResponse = {
  generatedAt: string;
  packageId: string;
  mediaId: string;
  profile: string;
  status: string;
  requeued: boolean;
};

export type ScheduleCheckIssue = {
  channelId: string;
  kind: string;
  startMs?: number;
  endMs?: number;
  mediaId?: string;
  message: string;
};

export type ScheduleCheckResponse = {
  generatedAt: string;
  windowFromMs: number;
  windowToMs: number;
  gapMs: number;
  channelsChecked: number;
  issues: ScheduleCheckIssue[];
};

export type MediaUpdateResponse = {
  mediaId: string;
  path: string;
  title: string;
  collectionName: string;
  seasonNumber?: number;
  episodeNumber?: number;
};

export type EncodeReclaimItem = {
  mediaId: string;
  packageId: string;
  profile: string;
  status: string;
  packageRoot?: string;
  bytes?: number;
  referenced: boolean;
  skipped: boolean;
  deleted: boolean;
};

export type EncodeReclaimResponse = {
  generatedAt: string;
  dryRun: boolean;
  force: boolean;
  candidates: number;
  deletedRows: number;
  skippedRows: number;
  totalBytes: number;
  items: EncodeReclaimItem[];
  warnings?: string[];
};

export type PlexStatus = {
  connected: boolean;
  username?: string;
  serverName?: string;
  url?: string;
  pathMap?: string;
};

export type PlexPinStart = {
  id: number;
  code: string;
  authUrl: string;
};

export type PlexServerConnection = {
  name: string;
  url: string;
  token: string;
  local: boolean;
};

export type PlexPinPoll = {
  authorized: boolean;
  username?: string;
  servers?: PlexServerConnection[];
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
  mediaKind: "movies" | "shows" | "music" | "filler";
  paths: string[];
  createdAtMs: number;
  updatedAtMs: number;
};

export type FillerAssetCandidateItem = {
  id: string;
  mediaId: string;
  label: string;
  kind: "filler" | "bumper" | "station_id" | string;
  durationMs: number;
  packageId?: string;
  packageStatus: string;
  packageReady: boolean;
  packagedDurationMs?: number;
};

export type FillerAssetCandidateList = {
  profile: string;
  count: number;
  assets: FillerAssetCandidateItem[];
};

export type ScheduleEntry = {
  entryId: string;
  mediaId: string;
  title?: string;
  path?: string;
  collectionName?: string;
  startMs: number;
  endMs: number;
  offsetMs?: number;
  durationMs: number;
};

export type ChannelMedia = {
  mediaId: string;
  title?: string;
  path?: string;
  collectionName?: string;
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
  collectionName?: string;
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

export type SubtitleWarning = {
  code: string;
  message: string;
  language?: string;
  title?: string;
  streamIndex?: number;
};

export type MediaPackageCandidate = {
  mediaId: string;
  title?: string;
  path: string;
  collectionName?: string;
  sourceRef?: string;
  durationMs: number;
  videoBitrateBps?: number;
  packageId?: string;
  packageStatus: "missing" | "pending" | "processing" | "failed" | string;
  packageProfile?: string;
  packageError?: string;
  packagedDurationMs?: number;
  packageBytes?: number;
  updatedAtMs?: number;
  selectable: boolean;
  subtitleWarnings?: SubtitleWarning[];
  sizeEstimate?: SizeEstimate;
};

// SizeEstimate is the estimated finished package size for a media under the
// selected profile. expectedBytes is meaningful only when expectedKnown is true
// (exact for copy/target/cbr; unknown for crf/capped-crf until an empirical
// bitrate exists). maxBytes is the worst-case ceiling.
export type SizeEstimate = {
  mode: "copy" | "crf" | "capped-crf" | "target" | "cbr" | "unknown";
  expectedBytes: number;
  expectedKnown: boolean;
  maxBytes: number;
};

export type MediaPackageCandidateList = {
  profile: string;
  count: number;
  // estimateSamples is the number of finished packages behind the empirical
  // expected-size estimate for this profile (CRF profiles only; 0 = no data, so
  // rows show a ceiling instead of an expected size).
  estimateSamples?: number;
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
  tags?: string[];
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
  subtitles?: {
    mode?: string;
    language?: string;
    fallback?: string;
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
  onDemandEncodings?: OnDemandEncodingItem[];
};

export type EncoderRegisterResponse = {
  id: string;
  name: string;
  apiKey: string;
  createdAtMs: number;
};

export type OnDemandEncodingItem = {
  encodingId: string;
  channelId: string;
  channelName: string;
  scheduleEntryId: string;
  mediaId: string;
  mediaTitle: string;
  profile: string;
  state: string;
  processRunning: boolean;
  spawnedAtMs: number;
  firstSegmentAtMs: number;
  lastProgressMs: number;
  segmentCount: number;
  updatedAtMs: number;
  lastError?: string;
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
  videoWidth: number;
  videoHeight: number;
  playerWidth: number;
  playerHeight: number;
  viewportWidth: number;
  viewportHeight: number;
  droppedFrames: number;
  totalFrames: number;
  bufferAhead: number;
  buffered: string;
  hlsLatency: number | null;
  liveSyncPosition: number | null;
  bandwidthEstimate: number | null;
  currentLevel: number | null;
  lastFrag: string;
  lastEvent: string;
  playbackEngine: string;
  errors: string[];
  streamUnavailable: boolean;
  streamUnavailableReason?: string;
  // A terminal, non-retryable playback failure for the current source (e.g. the
  // device can't decode the stream's video codec). Unlike streamUnavailable,
  // retrying won't help, so the UI shows it without a "retrying…" hint.
  fatalError?: string;
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
	playbackMode?: "packaged";
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
  prefillMode?: "eager" | "on_demand";
  adaptiveBitrate?: string;
  onImported: (channelId: string, result: { scheduleMode?: "back_to_back" | "slot_grid" | string }) => void;
};

export type ScheduleInsertItem = {
  mediaId: string;
  title?: string;
  path?: string;
  collectionName?: string;
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
