// Package playback owns the channel playback runtime: HLS manifests and
// artifacts, on-demand encodings, subtitles, direct play, channel metadata,
// readiness, and status. The composed server and admin module consume its
// narrow typed contracts instead of sharing implementation state or using
// loopback HTTP.
package playback

import "context"

// StatusProvider exposes a point-in-time playback status snapshot.
type StatusProvider interface {
	PlaybackStatus(context.Context) (Status, error)
}

// ReadinessChecker verifies that playback has enough current state and
// artifacts to serve every enabled channel according to its prefill mode.
type ReadinessChecker interface {
	CheckPlaybackReadiness(context.Context) error
}

// DegradedSignal describes one operator-attention signal and whether it has
// crossed its threshold. Degraded does not mean unhealthy — the instance is
// still serving, but something needs operator attention.
type DegradedSignal struct {
	Signal   string `json:"signal"`
	Degraded bool   `json:"degraded"`
	Detail   string `json:"detail"`
}

// DegradedReader exposes the current degraded-signal state for the operator
// recovery view. The admin module calls it through this typed interface rather
// than parsing Prometheus metrics.
type DegradedReader interface {
	DegradedSignals(ctx context.Context) ([]DegradedSignal, error)
}

// Controller exposes the narrow playback controls used by protected admin
// routes.
type Controller interface {
	StopOnDemandEncoding(channelID string)
}

// Status is the public playback status response and the in-process status
// contract consumed by admin.
type Status struct {
	NowMs     int64           `json:"nowMs"`
	StartedAt string          `json:"startedAt"`
	Channels  []ChannelStatus `json:"channels"`
}

// ChannelStatus describes the current playback state for one enabled channel.
type ChannelStatus struct {
	ID                     string `json:"id"`
	DisplayName            string `json:"displayName"`
	PrefillMode            string `json:"prefillMode"`
	RequiredPackageProfile string `json:"requiredPackageProfile"`
	HasSchedule            bool   `json:"hasSchedule"`
	PackageReady           bool   `json:"packageReady"`
	PackageError           string `json:"packageError,omitempty"`
	CurrentMediaID         string `json:"currentMediaID,omitempty"`
	CurrentMediaTitle      string `json:"currentMediaTitle,omitempty"`
}
