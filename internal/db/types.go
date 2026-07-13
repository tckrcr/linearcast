// Package db is the linearcast SQLite access layer.
//
// linearcast opens the database read-only. Writes (schema bootstrap, ingest,
// scheduling) live in cmd/ingest and other tools.
package db

import (
	"github.com/tckrcr/linearcast/internal/packageprofile"
)

const DefaultPackageProfile = packageprofile.DefaultName
const MusicPackageProfile = packageprofile.MusicName

// ScheduleGridMs is the wall-clock schedule grid. Schedule entry start_ms and
// duration_ms values must be divisible by this duration.
const ScheduleGridMs = int64(6000)

type MediaKind string

const (
	MediaKindVideo MediaKind = "video"
	MediaKindMusic MediaKind = "music"
)

// NormalizeMediaKind returns MediaKindMusic when kind is "music"; everything
// else maps to MediaKindVideo. Delegates to packageprofile so the rule lives
// in one place.
func NormalizeMediaKind(kind MediaKind) MediaKind {
	return MediaKind(packageprofile.NormalizeMediaKind(packageprofile.MediaKind(kind)))
}

func DefaultPackageProfileForMediaKind(kind MediaKind) string {
	if NormalizeMediaKind(kind) == MediaKindMusic {
		return MusicPackageProfile
	}
	return DefaultPackageProfile
}

type PlaybackMode string

const (
	PlaybackModeGenerated PlaybackMode = "generated"
	PlaybackModePackaged  PlaybackMode = "packaged"
)

type PackageStatus string

const (
	PackageStatusPending    PackageStatus = "pending"
	PackageStatusProcessing PackageStatus = "processing"
	PackageStatusReady      PackageStatus = "ready"
	PackageStatusFailed     PackageStatus = "failed"
)

type Channel struct {
	ID                     string
	DisplayName            string
	SourceDirectory        string
	Ordering               string
	Enabled                bool
	CreatedAtMs            int64
	Description            string
	HiddenFromGuide        bool
	ArtworkURL             string
	PlaybackMode           PlaybackMode
	RequiredPackageProfile string
	ABRLadder              []string
	PackagePrefillMs       *int64
	MediaKind              MediaKind
	ScheduleMode           string
	SlotDurationMs         *int64
	// UpstreamHLSURL is nil for a normal packaged channel; non-nil (the URL,
	// possibly empty) marks an external/live channel. The nil/non-nil split is
	// load-bearing — readers gate external-channel behavior on it.
	UpstreamHLSURL *string
	// PrefillMode controls how the channel's media is encoded:
	//   "eager"    — package the entire channel ahead of playback (default)
	//   "on_demand"— defer encoding until a viewer tunes in
	// On-demand channels schedule from codec-eligible media without requiring
	// ready packages and are excluded from eager packager discovery.
	PrefillMode string
}

// RequiresReadyPackages reports whether the channel's scheduled media must have
// ready linearcast packages. On-demand channels defer encoding via live
// channel encodings, so they don't require ready packages; only eager channels do. This
// is the single source of truth shared by the scheduler, the
// schedule builder, and the schedule-check audit — keep those call sites
// pointed here so the rule cannot drift.
func (c Channel) RequiresReadyPackages() bool {
	return c.PrefillMode == "eager"
}

type Media struct {
	ID             string
	Path           string
	Directory      string
	Title          string
	CollectionName string
	CollectionID   string
	SeasonNumber   *int64
	EpisodeNumber  *int64
	UserPreference *int64
	DurationMs     int64
	Container      string
	VideoCodec     string
	VideoWidth     int64 // 0 = unknown (pre-v29 ingest or audio-only)
	VideoHeight    int64
	// VideoBitrateBps is the source video bitrate in bits per second recorded at
	// ingest (0 = unknown). Used to estimate packaged size — exact for copy
	// profiles, since the video stream is remuxed unchanged.
	VideoBitrateBps int64
	// ColorTransfer and ColorPrimaries are ffprobe color metadata recorded for
	// HDR detection ("" = unknown). See codec.IsHDRTransfer.
	ColorTransfer  string
	ColorPrimaries string
	// CodecTagString is ffprobe's codec_tag_string for the video stream
	// ("" = unknown). Persisted for Dolby Vision Profile 5 detection. See
	// codec.IsDolbyVisionProfile5.
	CodecTagString   string
	AudioCodec       string
	CodecCheckPassed bool
	CodecCheckReason string
	IngestedAtMs     int64
	MediaKind        MediaKind // "" = video; "music" = audio-only
	SourceRef        string    // e.g. "plex://{ratingKey}" for media sourced from Plex
	Description      string
	ThumbPath        string
	ContentRating    string
	Genres           []string
}

type Collection struct {
	ID          string
	Name        string
	Kind        string
	Source      string
	Genres      []string
	CreatedAtMs int64
	UpdatedAtMs int64
}

type ChannelMediaRow struct {
	ChannelID string
	MediaID   string
	AddedAtMs int64
}

type ChannelMediaPackageRow struct {
	ChannelID          string
	MediaID            string
	AddedAtMs          int64
	Path               string
	Title              string
	CollectionName     string
	DurationMs         int64
	CodecCheckPassed   bool
	CodecCheckReason   string
	PackageID          *string
	PackageStatus      *string
	PackagedDurationMs *int64
	PackageError       *string
}

type FillerAsset struct {
	ID          string
	MediaID     string
	Label       string
	Kind        string
	Enabled     bool
	CreatedAtMs int64
}

type ChannelFillerAsset struct {
	FillerAsset
	ChannelID          string
	Weight             int64
	ChannelEnabled     bool
	Path               string
	Title              string
	CollectionName     string
	DurationMs         int64
	PackageID          *string
	PackageStatus      *string
	PackagedDurationMs *int64
	PackageError       *string
}

type ScheduleEntry struct {
	ID                    string
	ChannelID             string
	StartMs               int64
	MediaID               string
	OffsetMs              int64
	DurationMs            int64
	AnchorScheduleEntryID *string
	CreatedAtMs           int64
	// Kind is 'primary' or 'filler' (schedule_entries.entry_kind). An empty
	// value is treated as 'primary' on insert. Full-row readers populate it;
	// readers that don't select entry_kind leave it empty.
	Kind string
}

type PlayHistoryEntry struct {
	ID              int64
	ChannelID       string
	ScheduleEntryID string
	MediaID         string
	StartedAtMs     int64
	EndedAtMs       int64
	DurationMs      int64
	MediaTitle      string
	MediaPath       string
}

type MediaPackage struct {
	ID                 string
	MediaID            string
	RenditionProfile   string
	Status             PackageStatus
	PackageRoot        *string
	InitSegmentPath    *string
	SegmentBasePath    string
	Container          string
	VideoCodec         string
	VideoProfile       string
	VideoWidth         *int64
	VideoHeight        *int64
	AudioCodec         string
	AudioProfile       string
	Timescale          *int64
	PackagedDurationMs *int64
	PackageBytes       *int64 // on-disk package size recorded at finalize (nil = unknown)
	Error              *string
	LastAttemptError   *string
	Attempts           int64
	CreatedAtMs        int64
	UpdatedAtMs        int64
}

type PackagedSegment struct {
	PackageID       string
	SegmentNumber   int64
	MediaStartMs    int64
	DurationMs      int64
	Path            *string
	ByteRangeStart  *int64
	ByteRangeLength *int64
}

type PackageStatusSummary struct {
	Status string
	Count  int64
}

type PackageProfileSummary struct {
	RenditionProfile string
	Status           string
	PackageCount     int64
	ReadyDurationMs  int64
	OldestUpdatedMs  *int64
	NewestUpdatedMs  *int64
}

type PackageRoot struct {
	RenditionProfile string
	Status           string
	PackageRoot      string
}

type MediaPackageFailure struct {
	MediaID string
	Code    string
	Message string
}

type MediaPackageRequestResult struct {
	Profile        string
	Queued         []string
	AlreadyPending []string
	AlreadyReady   []string
	Failed         []MediaPackageFailure
}

type MediaPackageCancelResult struct {
	Profile            string
	CanceledPending    int64
	CanceledProcessing int64
	SkippedReady       int64
	SkippedFailed      int64
	SkippedMissing     int64
	SkippedUnsupported int64
	AffectedMediaIDs   []string
}

type MediaPackageCandidate struct {
	MediaID            string
	Path               string
	Title              string
	CollectionName     string
	SourceRef          string
	DurationMs         int64
	VideoBitrateBps    int64 // source video bitrate (0 = unknown); feeds copy-profile size estimates
	PackageID          *string
	RenditionProfile   string
	PackageStatus      *string
	PackageError       *string
	PackagedDurationMs *int64
	PackageBytes       *int64
	UpdatedAtMs        *int64
}

type ChannelPackageSummary struct {
	ChannelID        string
	DisplayName      string
	RenditionProfile string
	Status           string
	PackageCount     int64
	ReadyDurationMs  int64
	OldestUpdatedMs  *int64
	NewestUpdatedMs  *int64
}

type ChannelPackageNeedSummary struct {
	ChannelID        string
	DisplayName      string
	RenditionProfile string
	NeededCount      int64
	ReadyCount       int64
	ProcessingCount  int64
	PendingCount     int64
	FailedCount      int64
	MissingCount     int64
}

type ChannelPackageRoot struct {
	ChannelID        string
	RenditionProfile string
	Status           string
	PackageRoot      string
}

// TrackSource identifies where a subtitle track came from.
type TrackSource string

const (
	TrackSourceEmbedded       TrackSource = "embedded_text"
	TrackSourceEmbeddedBitmap TrackSource = "embedded_bitmap"
	TrackSourceManual         TrackSource = "manual"
)

// PackageTrack represents one subtitle or audio track generated for a package.
// Embedded tracks have StreamIndex >= 0 (the stream's index in the source file).
// Manually sourced tracks use StreamIndex = -1. A Source = embedded_bitmap row is
// package-scoped inventory for a non-text subtitle stream (PGS/VOBSUB); it always
// has Path == nil and is usable only for burn-in.
type PackageTrack struct {
	ID          int64
	PackageID   string
	Kind        string
	StreamIndex int
	Language    string
	Title       string
	Codec       string
	Source      TrackSource
	DefaultFlag bool
	// Forced marks a forced-display (foreign-dialogue) subtitle track.
	Forced bool
	// HearingImpaired marks an SDH (subtitles for the deaf/hard of hearing) track.
	// Within the same language and source tier, SDH is preferred for playback.
	HearingImpaired bool
	Path            *string
}

type ChannelWrite struct {
	ID                     string
	DisplayName            string
	SourceDirectory        string
	Ordering               string
	PlaybackMode           PlaybackMode
	RequiredPackageProfile string
	ABRLadder              []string
	PackagePrefillMs       *int64
	CreatedAtMs            int64
	MediaKind              MediaKind
	ScheduleMode           string
	SlotDurationMs         *int64
	UpstreamHLSURL         *string
	PrefillMode            string
}

// ScheduleEntryEnriched is a ScheduleEntry joined with its media row.
type ScheduleEntryEnriched struct {
	ID             string
	ChannelID      string
	StartMs        int64
	MediaID        string
	OffsetMs       int64
	DurationMs     int64
	CreatedAtMs    int64
	Path           string
	Title          string
	CollectionName string
	Description    string
	ThumbPath      string
	ContentRating  string
	Genres         []string
	// SeasonNumber and EpisodeNumber carry the source media's episode metadata
	// (nil for movies / non-episodic media). Used to emit XMLTV <episode-num>.
	SeasonNumber  *int64
	EpisodeNumber *int64
}

// GroupCursor is the per-scheduling_group cursor derived from a channel's
// existing schedule_entries. LastMediaID is the most recently scheduled
// media in this group (across past + future entries); LastEndMs is the
// end_ms of that entry. Groups absent from the channel's schedule do not
// appear in the map; treat their cursor as "start of group at -inf".
type GroupCursor struct {
	LastMediaID string
	LastEndMs   int64
}

type InvalidProfilePackage struct {
	ID               string
	MediaID          string
	RenditionProfile string
	Status           string
	PackageRoot      string
}

// ProfileReadiness summarises how much of a channel's media is packaged at a
// given profile. Total counts only codec-check-passing media.
type ProfileReadiness struct {
	Profile    string
	Total      int64
	Ready      int64
	Pending    int64
	Processing int64
	Failed     int64
	Missing    int64
}

type PackageProfileRecord struct {
	Profile   packageprofile.Profile
	IsBuiltin bool
	Disabled  bool
}

type PackageProfileReferences struct {
	MediaPackages   int64 `json:"mediaPackages"`
	Channels        int64 `json:"channels"`
	ScheduleEntries int64 `json:"scheduleEntries"`
}
