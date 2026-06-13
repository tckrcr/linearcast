package admin

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/scheduler"
)

// Sentinel errors returned by scheduleService methods. Handlers map these to
// specific HTTP status codes and error codes in the response body.
var (
	errChannelNotFound     = errors.New("channel not found")
	errEntryNotFound       = errors.New("schedule entry not found")
	errMediaNotInChannel   = errors.New("media does not belong to this channel")
	errPackageNotReady     = errors.New("media does not have a ready package for this channel profile")
	errInsideScheduleEntry = errors.New("time falls inside an existing schedule entry")
	errNoScheduleGap       = errors.New("time is not the start of a fillable schedule gap")
	errFillerTooShort      = errors.New("filler media is too short for the schedule gap")
	errScheduleEntryLocked = errors.New("schedule entry is not editable")
	errNotSlotGrid         = errors.New("channel is not in slot_grid schedule mode")
)

type restartResult struct {
	Cleared    int64
	Inserted   int64
	LastEndMs  int64
	NoPackages bool
}

type deleteEntryResult struct {
	Inserted   int64
	NoPackages bool
}

type deleteRangeResult struct {
	// NoOp is true when no schedule entries intersect [fromMs, toMs).
	// All other fields are zero in that case.
	NoOp               bool
	Deleted            int64
	ClearedTail        int64
	Inserted           int64
	LastEndMs          int64
	RebuildStartMs     int64
	ResumeAfterMediaID string
	NoPackages         bool
}

type upsertEntryResult struct {
	DurationMs     int64
	PackageProfile string
	Cleared        int64
	Inserted       int64
	LastEndMs      int64
	NoPackages     bool
}

type fillGapResult struct {
	EntryID        string
	StartMs        int64
	EndMs          int64
	DurationMs     int64
	OffsetMs       int64
	MediaID        string
	PackageProfile string
}

type saveWindowResult struct {
	Cleared            int64
	Inserted           int64
	LastEndMs          int64
	ResumeAfterMediaID string
	NoPackages         bool
}

type recomposeResult struct {
	FromMs    int64
	Cleared   int64
	Inserted  int64
	LastEndMs int64
	// Gappy is true when the recomposed future still contains gaps — the
	// gap-free fallback when the channel has no attached, ready filler. The
	// rebuild still WARN-logs in scheduler.BuildEntriesSlotGridFilled.
	Gappy bool
}

type insertRelativeResult struct {
	EntryID    string
	StartMs    int64
	DurationMs int64
}

// removeMediaResult holds the outcome of a RemoveMedia call.
// When RebuildStartMs is zero, no schedule prune occurred (either no future
// entries existed or pruneSchedule was false).
type removeMediaResult struct {
	Pruned         int64
	RebuildStartMs int64
	Inserted       int64
	NoPackages     bool
}

// scheduleService owns the write workflows for admin schedule editing.
type scheduleService struct {
	db  *sql.DB
	now func() time.Time
}

func newScheduleService(db *sql.DB, now func() time.Time) *scheduleService {
	return &scheduleService{db: db, now: now}
}

func (s *scheduleService) scheduleMediaAllowed(ctx context.Context, channelID, mediaID string) (bool, error) {
	belongs, err := db.ChannelMediaExists(ctx, s.db, channelID, mediaID)
	if err != nil || belongs {
		return belongs, err
	}
	return db.ChannelFillerAssetMediaExists(ctx, s.db, channelID, mediaID)
}

// Restart clears the channel schedule and immediately re-extends it within a
// single transaction so a partial failure leaves the schedule unchanged.
func (s *scheduleService) Restart(ctx context.Context, channelID string) (restartResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return restartResult{}, err
	}
	if ch == nil {
		return restartResult{}, errChannelNotFound
	}

	var res restartResult
	var extResult scheduler.ExtendResult
	var extErr error

	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		var txErr error
		res.Cleared, txErr = db.ClearSchedule(ctx, tx, channelID)
		if txErr != nil {
			return txErr
		}
		extResult, extErr = scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:  24,
			InTransaction: true,
		})
		if extErr != nil && !errors.Is(extErr, scheduler.ErrNoReadyPackages) {
			return extErr
		}
		return nil
	})
	if err != nil {
		return restartResult{}, err
	}

	res.Inserted = int64(extResult.Inserted)
	res.LastEndMs = extResult.LastEndMs
	res.NoPackages = errors.Is(extErr, scheduler.ErrNoReadyPackages)
	return res, nil
}

// DeleteEntry removes a schedule entry by its stable ID. When rebuild is true,
// it clears and re-extends the tail so start times remain contiguous. Returns
// errEntryNotFound if the entry does not exist.
func (s *scheduleService) DeleteEntry(ctx context.Context, channelID string, entryID string, rebuild bool) (deleteEntryResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return deleteEntryResult{}, err
	}
	if ch == nil {
		return deleteEntryResult{}, errChannelNotFound
	}

	// Load the entry to get startMs (needed for tail rebuild) and mediaID.
	entries, err := db.ScheduleEntryByID(ctx, s.db, entryID)
	if err != nil {
		return deleteEntryResult{}, err
	}
	if entries == nil {
		return deleteEntryResult{}, errEntryNotFound
	}
	if entries.ChannelID != channelID {
		return deleteEntryResult{}, errEntryNotFound
	}
	startMs := entries.StartMs
	deletedMediaID := entries.MediaID

	if !rebuild {
		var found bool
		err := db.WithTx(ctx, s.db, func(tx db.Execer) error {
			var txErr error
			found, txErr = db.DeleteScheduleEntryByID(ctx, tx, entryID)
			return txErr
		})
		if err != nil {
			return deleteEntryResult{}, err
		}
		if !found {
			return deleteEntryResult{}, errEntryNotFound
		}
		return deleteEntryResult{}, nil
	}

	var res deleteEntryResult
	var extResult scheduler.ExtendResult
	var extErr error

	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		found, txErr := db.DeleteScheduleEntryByID(ctx, tx, entryID)
		if txErr != nil {
			return txErr
		}
		if !found {
			return errEntryNotFound
		}
		if _, txErr := db.ClearScheduleAfter(ctx, tx, channelID, startMs); txErr != nil {
			return txErr
		}
		extResult, extErr = scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:       24,
			InTransaction:      true,
			ResumeAfterMediaID: deletedMediaID,
		})
		if extErr != nil && !errors.Is(extErr, scheduler.ErrNoReadyPackages) {
			return extErr
		}
		return nil
	})
	if err != nil {
		return deleteEntryResult{}, err
	}

	res.Inserted = int64(extResult.Inserted)
	res.NoPackages = errors.Is(extErr, scheduler.ErrNoReadyPackages)
	return res, nil
}

// DeleteRange removes all schedule entries that intersect [fromMs, toMs). When
// rebuild is true, it re-extends the schedule from the earliest removed entry.
// Returns a NoOp result (all zero) when no entries fall in the range.
func (s *scheduleService) DeleteRange(ctx context.Context, channelID string, fromMs, toMs int64, rebuild bool) (deleteRangeResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return deleteRangeResult{}, err
	}
	if ch == nil {
		return deleteRangeResult{}, errChannelNotFound
	}

	targets, err := db.ScheduleWindow(ctx, s.db, channelID, fromMs, toMs)
	if err != nil {
		return deleteRangeResult{}, err
	}
	if len(targets) == 0 {
		return deleteRangeResult{NoOp: true}, nil
	}
	if !rebuild {
		var deleted int64
		err := db.WithTx(ctx, s.db, func(tx db.Execer) error {
			var txErr error
			deleted, txErr = db.DeleteScheduleRangeIntersect(ctx, tx, channelID, fromMs, toMs)
			return txErr
		})
		if err != nil {
			return deleteRangeResult{}, err
		}
		return deleteRangeResult{Deleted: deleted}, nil
	}

	rebuildStartMs := targets[0].StartMs
	resumeAfterMediaID := targets[0].MediaID
	latestStartMs := targets[0].StartMs
	for _, t := range targets[1:] {
		if t.StartMs < rebuildStartMs {
			rebuildStartMs = t.StartMs
		}
		if t.StartMs >= latestStartMs {
			latestStartMs = t.StartMs
			resumeAfterMediaID = t.MediaID
		}
	}

	res := deleteRangeResult{
		RebuildStartMs:     rebuildStartMs,
		ResumeAfterMediaID: resumeAfterMediaID,
	}
	var extResult scheduler.ExtendResult
	var extErr error

	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		var txErr error
		res.Deleted, txErr = db.DeleteScheduleRangeIntersect(ctx, tx, channelID, fromMs, toMs)
		if txErr != nil {
			return txErr
		}
		res.ClearedTail, txErr = db.ClearScheduleAfter(ctx, tx, channelID, rebuildStartMs)
		if txErr != nil {
			return txErr
		}
		extResult, extErr = scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:       24,
			InTransaction:      true,
			ResumeAfterMediaID: resumeAfterMediaID,
		})
		if extErr != nil && !errors.Is(extErr, scheduler.ErrNoReadyPackages) {
			return extErr
		}
		return nil
	})
	if err != nil {
		return deleteRangeResult{}, err
	}

	res.Inserted = int64(extResult.Inserted)
	res.LastEndMs = extResult.LastEndMs
	res.NoPackages = errors.Is(extErr, scheduler.ErrNoReadyPackages)
	return res, nil
}

// UpsertEntry inserts a single manual schedule entry at startMs, clears the
// existing tail, and re-extends the schedule. Returns errMediaNotInChannel or
// errPackageNotReady for domain pre-condition failures.
func (s *scheduleService) UpsertEntry(ctx context.Context, channelID string, req scheduleEntryWriteRequest) (upsertEntryResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return upsertEntryResult{}, err
	}
	if ch == nil {
		return upsertEntryResult{}, errChannelNotFound
	}

	entries, err := db.ScheduleWindow(ctx, s.db, channelID, req.StartMs, req.StartMs+1)
	if err != nil {
		return upsertEntryResult{}, err
	}
	for _, e := range entries {
		if e.StartMs < req.StartMs && req.StartMs < e.StartMs+e.DurationMs {
			return upsertEntryResult{}, errInsideScheduleEntry
		}
	}

	belongs, err := s.scheduleMediaAllowed(ctx, channelID, req.MediaID)
	if err != nil {
		return upsertEntryResult{}, err
	}
	if !belongs {
		return upsertEntryResult{}, errMediaNotInChannel
	}

	profile := requiredPackageProfile(*ch)
	pkg, err := db.ReadyMediaPackage(ctx, s.db, req.MediaID, profile)
	if err != nil {
		return upsertEntryResult{}, err
	}
	if pkg == nil || pkg.PackagedDurationMs == nil {
		return upsertEntryResult{}, errPackageNotReady
	}
	durationMs := scheduler.ClipTo6s(*pkg.PackagedDurationMs)
	if durationMs <= 0 {
		return upsertEntryResult{}, errPackageNotReady
	}

	prevEntry, err := db.LastScheduleEntryBefore(ctx, s.db, channelID, req.StartMs)
	if err != nil {
		return upsertEntryResult{}, err
	}

	res := upsertEntryResult{DurationMs: durationMs, PackageProfile: profile}
	var extResult scheduler.ExtendResult
	var extErr error

	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		var txErr error
		res.Cleared, txErr = db.ClearScheduleAfter(ctx, tx, channelID, req.StartMs)
		if txErr != nil {
			return txErr
		}
		entry := db.ScheduleEntry{
			ChannelID:   channelID,
			StartMs:     req.StartMs,
			MediaID:     req.MediaID,
			OffsetMs:    0,
			DurationMs:  durationMs,
			CreatedAtMs: s.now().UTC().UnixMilli(),
		}
		if prevEntry != nil {
			entry.AnchorScheduleEntryID = &prevEntry.ID
		}
		insertedManual, txErr := db.InsertScheduleEntries(ctx, tx, []db.ScheduleEntry{entry})
		if txErr != nil {
			return txErr
		}
		extResult, extErr = scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:       24,
			InTransaction:      true,
			ResumeAfterMediaID: req.MediaID,
		})
		if extErr != nil && !errors.Is(extErr, scheduler.ErrNoReadyPackages) {
			return extErr
		}
		res.Inserted = int64(insertedManual) + int64(extResult.Inserted)
		res.LastEndMs = extResult.LastEndMs
		return nil
	})
	if err != nil {
		return upsertEntryResult{}, err
	}

	res.NoPackages = errors.Is(extErr, scheduler.ErrNoReadyPackages)
	return res, nil
}

func (s *scheduleService) FillGap(ctx context.Context, channelID string, req scheduleGapFillRequest) (fillGapResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return fillGapResult{}, err
	}
	if ch == nil {
		return fillGapResult{}, errChannelNotFound
	}
	if req.StartMs%segmentMs != 0 || req.OffsetMs%segmentMs != 0 {
		return fillGapResult{}, errNoScheduleGap
	}
	if req.OffsetMs < 0 {
		return fillGapResult{}, errFillerTooShort
	}

	belongs, err := db.ChannelFillerAssetMediaExists(ctx, s.db, channelID, req.MediaID)
	if err != nil {
		return fillGapResult{}, err
	}
	if !belongs {
		// Auto-attach: if the media is a registered filler asset that matches the
		// channel's kind, attach it now rather than forcing a separate step.
		asset, assetErr := db.FillerAssetByMediaID(ctx, s.db, req.MediaID)
		if assetErr != nil {
			if errors.Is(assetErr, sql.ErrNoRows) {
				return fillGapResult{}, errMediaNotInChannel
			}
			return fillGapResult{}, assetErr
		}
		media, mediaErr := db.MediaByID(ctx, s.db, asset.MediaID)
		if mediaErr != nil {
			return fillGapResult{}, mediaErr
		}
		if media == nil || db.NormalizeMediaKind(media.MediaKind) != ch.MediaKind {
			return fillGapResult{}, errMediaNotInChannel
		}
		if attachErr := db.AttachChannelFillerAsset(ctx, s.db, channelID, asset.ID, 1, true); attachErr != nil {
			return fillGapResult{}, attachErr
		}
	}

	prevEntry, err := db.LastScheduleEntryBefore(ctx, s.db, channelID, req.StartMs+1)
	if err != nil {
		return fillGapResult{}, err
	}
	if prevEntry != nil && prevEntry.StartMs < req.StartMs && req.StartMs < prevEntry.StartMs+prevEntry.DurationMs {
		return fillGapResult{}, errInsideScheduleEntry
	}
	nextEntry, err := db.NextScheduleEntryAfter(ctx, s.db, channelID, req.StartMs)
	if err != nil {
		return fillGapResult{}, err
	}
	if nextEntry == nil || nextEntry.StartMs <= req.StartMs {
		return fillGapResult{}, errNoScheduleGap
	}
	if prevEntry != nil && prevEntry.StartMs == req.StartMs {
		return fillGapResult{}, errNoScheduleGap
	}

	durationMs := nextEntry.StartMs - req.StartMs
	if durationMs <= 0 || durationMs%segmentMs != 0 {
		return fillGapResult{}, errNoScheduleGap
	}
	if prevEntry != nil && req.StartMs < prevEntry.StartMs+prevEntry.DurationMs {
		return fillGapResult{}, errInsideScheduleEntry
	}

	profile := requiredPackageProfile(*ch)
	pkg, err := db.ReadyMediaPackage(ctx, s.db, req.MediaID, profile)
	if err != nil {
		return fillGapResult{}, err
	}
	if pkg == nil || pkg.PackagedDurationMs == nil {
		return fillGapResult{}, errPackageNotReady
	}
	packagedDurationMs := scheduler.ClipTo6s(*pkg.PackagedDurationMs)
	offsetMs, err := s.resolveFillerOffset(ctx, channelID, req, durationMs, packagedDurationMs)
	if err != nil {
		return fillGapResult{}, err
	}
	if offsetMs+durationMs > packagedDurationMs {
		return fillGapResult{}, errFillerTooShort
	}

	entryID := uuid.NewString()
	entry := db.ScheduleEntry{
		ID:          entryID,
		ChannelID:   channelID,
		StartMs:     req.StartMs,
		MediaID:     req.MediaID,
		OffsetMs:    offsetMs,
		DurationMs:  durationMs,
		CreatedAtMs: s.now().UTC().UnixMilli(),
		Kind:        "filler",
	}
	if prevEntry != nil {
		entry.AnchorScheduleEntryID = &prevEntry.ID
	}

	if err := db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		// Repoint the successor before inserting the filler row. The chain has a
		// unique successor-per-anchor index, so inserting filler anchored to the
		// previous row while the successor still uses that same anchor would violate
		// idx_schedule_entries_anchor. There is no FK on anchor_schedule_entry_id,
		// so this transient in-transaction forward reference is safe and rolls back
		// with the insert if anything fails.
		if _, err := tx.ExecContext(ctx, `UPDATE schedule_entries SET anchor_schedule_entry_id = ? WHERE channel_id = ? AND id = ?`, entryID, channelID, nextEntry.ID); err != nil {
			return err
		}
		if _, err := db.InsertScheduleEntries(ctx, tx, []db.ScheduleEntry{entry}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fillGapResult{}, err
	}

	return fillGapResult{
		EntryID:        entryID,
		StartMs:        req.StartMs,
		EndMs:          req.StartMs + durationMs,
		DurationMs:     durationMs,
		OffsetMs:       offsetMs,
		MediaID:        req.MediaID,
		PackageProfile: profile,
	}, nil
}

// resolveFillerOffset chooses the filler asset offset for a gap fill. The
// default ("" / "zero") honors the client-provided OffsetMs. "sequential"
// continues the rotation from where the previous placement of the same filler
// asset on this channel left off, wrapping back to the asset start when
// continuing would overrun its packaged duration. This stops a long filler
// asset from replaying its opening seconds in every gap while staying
// deterministic across schedule rebuilds.
//
// "previous placement" is the most recent filler entry for the same media
// before this gap: db.LastEntryWithMediaBefore filters on entry_kind='filler',
// so any incidental reuse of the same media as primary programming is ignored.
func (s *scheduleService) resolveFillerOffset(ctx context.Context, channelID string, req scheduleGapFillRequest, durationMs, packagedDurationMs int64) (int64, error) {
	if req.OffsetMode != "sequential" {
		return req.OffsetMs, nil
	}
	if durationMs >= packagedDurationMs {
		// The gap is at least as long as the whole asset; only offset 0 can fit,
		// and the caller's fit check rejects it when even that overruns.
		return 0, nil
	}
	prev, err := db.LastEntryWithMediaBefore(ctx, s.db, channelID, req.MediaID, req.StartMs)
	if err != nil {
		return 0, err
	}
	if prev == nil {
		return 0, nil
	}
	cursor := (prev.OffsetMs + prev.DurationMs) % packagedDurationMs
	if cursor+durationMs > packagedDurationMs {
		cursor = 0
	}
	return cursor, nil
}

func (s *scheduleService) saveWindowByMediaIDs(ctx context.Context, channelID string, fromMs, toMs int64, tailMode string, extendTail bool, mediaIDs []string) (saveWindowResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return saveWindowResult{}, err
	}
	if ch == nil {
		return saveWindowResult{}, errChannelNotFound
	}

	if entries, err := db.ScheduleWindow(ctx, s.db, channelID, fromMs, fromMs+1); err != nil {
		return saveWindowResult{}, err
	} else {
		for _, e := range entries {
			if e.StartMs < fromMs && fromMs < e.StartMs+e.DurationMs {
				return saveWindowResult{}, errInsideScheduleEntry
			}
		}
	}

	originalTargets, err := db.ScheduleWindow(ctx, s.db, channelID, fromMs, toMs)
	if err != nil {
		return saveWindowResult{}, err
	}

	profile := requiredPackageProfile(*ch)
	readyByMedia := make(map[string]readyScheduleMedia, len(mediaIDs))
	for _, mediaID := range mediaIDs {
		mediaID = strings.TrimSpace(mediaID)
		if mediaID == "" {
			return saveWindowResult{}, errors.New("media id is required")
		}
		if _, ok := readyByMedia[mediaID]; ok {
			continue
		}
		belongs, err := s.scheduleMediaAllowed(ctx, channelID, mediaID)
		if err != nil {
			return saveWindowResult{}, err
		}
		if !belongs {
			return saveWindowResult{}, errMediaNotInChannel
		}
		pkg, err := db.ReadyMediaPackage(ctx, s.db, mediaID, profile)
		if err != nil {
			return saveWindowResult{}, err
		}
		if pkg == nil || pkg.PackagedDurationMs == nil {
			return saveWindowResult{}, errPackageNotReady
		}
		durationMs := scheduler.ClipTo6s(*pkg.PackagedDurationMs)
		if durationMs <= 0 {
			return saveWindowResult{}, errPackageNotReady
		}
		readyByMedia[mediaID] = readyScheduleMedia{mediaID: mediaID, durationMs: durationMs}
	}

	nowMs := s.now().UTC().UnixMilli()
	prevEntry, err := db.LastScheduleEntryBefore(ctx, s.db, channelID, fromMs)
	if err != nil {
		return saveWindowResult{}, err
	}
	replacementEntries := make([]db.ScheduleEntry, 0, len(mediaIDs))
	cursor := fromMs
	for _, mediaID := range mediaIDs {
		ready := readyByMedia[strings.TrimSpace(mediaID)]
		replacementEntries = append(replacementEntries, db.ScheduleEntry{
			ID:          uuid.NewString(),
			ChannelID:   channelID,
			StartMs:     cursor,
			MediaID:     ready.mediaID,
			OffsetMs:    0,
			DurationMs:  ready.durationMs,
			CreatedAtMs: nowMs,
		})
		cursor += ready.durationMs
	}
	if len(replacementEntries) > 0 && prevEntry != nil {
		replacementEntries[0].AnchorScheduleEntryID = &prevEntry.ID
	}

	resumeAfterMediaID := ""
	if len(replacementEntries) > 0 {
		resumeAfterMediaID = replacementEntries[len(replacementEntries)-1].MediaID
	}
	if tailMode == "preserve" && len(originalTargets) > 0 {
		latest := originalTargets[0]
		for _, t := range originalTargets[1:] {
			if t.StartMs >= latest.StartMs {
				latest = t
			}
		}
		resumeAfterMediaID = latest.MediaID
	}

	res := saveWindowResult{ResumeAfterMediaID: resumeAfterMediaID}
	var extResult scheduler.ExtendResult
	var extErr error

	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		var txErr error
		if !extendTail {
			var nextID sql.NullString
			res.Cleared, _, nextID, txErr = db.DeleteScheduleRangeIntersectForRewrite(ctx, tx, channelID, fromMs, toMs)
			if txErr != nil {
				return txErr
			}
			insertedManual, txErr := db.InsertScheduleEntries(ctx, tx, replacementEntries)
			if txErr != nil {
				return txErr
			}
			res.Inserted = int64(insertedManual)
			res.LastEndMs = fromMs
			for _, entry := range replacementEntries {
				if endMs := entry.StartMs + entry.DurationMs; endMs > res.LastEndMs {
					res.LastEndMs = endMs
				}
			}
			if nextID.Valid {
				anchor := sql.NullString{}
				if len(replacementEntries) > 0 {
					anchor = sql.NullString{String: replacementEntries[len(replacementEntries)-1].ID, Valid: true}
				} else if prevEntry != nil {
					anchor = sql.NullString{String: prevEntry.ID, Valid: true}
				}
				if _, txErr := tx.ExecContext(ctx,
					`UPDATE schedule_entries SET anchor_schedule_entry_id = ? WHERE id = ?`,
					anchor, nextID.String,
				); txErr != nil {
					return txErr
				}
			}
			return nil
		}
		res.Cleared, txErr = db.ClearScheduleAfter(ctx, tx, channelID, fromMs)
		if txErr != nil {
			return txErr
		}
		insertedManual, txErr := db.InsertScheduleEntries(ctx, tx, replacementEntries)
		if txErr != nil {
			return txErr
		}
		extResult, extErr = scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:       24,
			InTransaction:      true,
			ResumeAfterMediaID: resumeAfterMediaID,
		})
		if extErr != nil && !errors.Is(extErr, scheduler.ErrNoReadyPackages) {
			return extErr
		}
		res.Inserted = int64(insertedManual) + int64(extResult.Inserted)
		res.LastEndMs = extResult.LastEndMs
		return nil
	})
	if err != nil {
		return saveWindowResult{}, err
	}

	res.NoPackages = errors.Is(extErr, scheduler.ErrNoReadyPackages)
	return res, nil
}

// SaveWindowOrdered replaces schedule entries in [fromMs, toMs) using only the
// draft order. The service computes the contiguous start_ms values from the
// order and current package durations so the UI does not have to rebuild them.
func (s *scheduleService) SaveWindowOrdered(ctx context.Context, channelID string, req scheduleWindowSaveOrderedRequest) (saveWindowResult, error) {
	mediaIDs := make([]string, 0, len(req.Entries))
	for _, entry := range req.Entries {
		mediaIDs = append(mediaIDs, entry.MediaID)
	}
	extendTail := req.ExtendTail == nil || *req.ExtendTail
	return s.saveWindowByMediaIDs(ctx, channelID, req.FromMs, req.ToMs, req.TailMode, extendTail, mediaIDs)
}

// RecomposeSlotGridFuture rebuilds a slot-grid channel's future schedule
// gap-free in one atomic operation: it clears everything after the currently
// in-progress entry (preserving live playback) and re-extends through the
// scheduler's slot tiler, which lays primaries on slot boundaries and tiles
// filler from the channel's attached filler pool. This is the existing-channel
// edit-path equivalent of the gap-free create flow — it replaces the old
// one-gap-at-a-time FillGap editing.
//
// If the channel has no eligible ready packages the whole operation rolls back
// (the schedule is never wiped without a rebuild). When the channel has primary
// media but no usable filler, the rebuild succeeds with gaps (the documented
// fallback) and the result is marked Gappy.
func (s *scheduleService) RecomposeSlotGridFuture(ctx context.Context, channelID string) (recomposeResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return recomposeResult{}, err
	}
	if ch == nil {
		return recomposeResult{}, errChannelNotFound
	}
	if ch.ScheduleMode != "slot_grid" {
		return recomposeResult{}, errNotSlotGrid
	}

	nowMs := s.now().UTC().UnixMilli()
	// Preserve the most recently started entry (the in-progress program) and
	// clear everything after it; rebuild from there. When nothing is at/before
	// now, clear from the current grid boundary and rebuild forward.
	prev, err := db.LastScheduleEntryBefore(ctx, s.db, channelID, nowMs+1)
	if err != nil {
		return recomposeResult{}, err
	}
	fromMs := scheduler.Align6s(nowMs)
	if prev != nil {
		fromMs = prev.StartMs + prev.DurationMs
	}

	res := recomposeResult{FromMs: fromMs}
	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		ext, e := scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:  24,
			ClearAfterMs:  sql.NullInt64{Int64: fromMs, Valid: true},
			NowMs:         nowMs,
			InTransaction: true,
		})
		if e != nil {
			// Roll back the clear too — never leave the channel with a wiped
			// future and no rebuild (e.g. ErrNoReadyPackages).
			return e
		}
		res.Cleared = ext.Cleared
		res.Inserted = int64(ext.Inserted)
		res.LastEndMs = ext.LastEndMs

		gaps, gerr := db.ScheduleGaps(ctx, tx, channelID, fromMs, ext.LastEndMs)
		if gerr != nil {
			return gerr
		}
		res.Gappy = len(gaps) > 0
		return nil
	})
	if err != nil {
		return recomposeResult{}, err
	}
	return res, nil
}

// InsertEntryAfter inserts a new schedule entry directly after the target row
// and shifts the suffix forward by the inserted duration.
func (s *scheduleService) InsertEntryAfter(ctx context.Context, channelID, afterEntryID, mediaID string) (insertRelativeResult, error) {
	return s.insertEntryRelative(ctx, channelID, afterEntryID, mediaID, true)
}

// InsertEntryBefore inserts a new schedule entry directly before the target
// row and shifts the suffix forward by the inserted duration.
func (s *scheduleService) InsertEntryBefore(ctx context.Context, channelID, beforeEntryID, mediaID string) (insertRelativeResult, error) {
	return s.insertEntryRelative(ctx, channelID, beforeEntryID, mediaID, false)
}

func (s *scheduleService) insertEntryRelative(ctx context.Context, channelID, targetEntryID, mediaID string, after bool) (insertRelativeResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return insertRelativeResult{}, err
	}
	if ch == nil {
		return insertRelativeResult{}, errChannelNotFound
	}

	belongs, err := s.scheduleMediaAllowed(ctx, channelID, mediaID)
	if err != nil {
		return insertRelativeResult{}, err
	}
	if !belongs {
		return insertRelativeResult{}, errMediaNotInChannel
	}

	profile := requiredPackageProfile(*ch)
	pkg, err := db.ReadyMediaPackage(ctx, s.db, mediaID, profile)
	if err != nil {
		return insertRelativeResult{}, err
	}
	if pkg == nil || pkg.PackagedDurationMs == nil {
		return insertRelativeResult{}, errPackageNotReady
	}
	durationMs := scheduler.ClipTo6s(*pkg.PackagedDurationMs)
	if durationMs <= 0 {
		return insertRelativeResult{}, errPackageNotReady
	}

	nowMs := s.now().UTC().UnixMilli()
	var inserted insertRelativeResult
	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		target, txErr := db.ScheduleEntryByID(ctx, tx, targetEntryID)
		if txErr != nil {
			return txErr
		}
		if target == nil || target.ChannelID != channelID {
			return errEntryNotFound
		}
		if after {
			if target.StartMs+target.DurationMs <= nowMs {
				return errScheduleEntryLocked
			}
		} else {
			if target.StartMs <= nowMs {
				return errScheduleEntryLocked
			}
		}

		insertionStartMs := target.StartMs
		if after {
			insertionStartMs += target.DurationMs
		}

		var successorID sql.NullString
		if after {
			if txErr := tx.QueryRowContext(ctx, `
				SELECT id FROM schedule_entries
				WHERE channel_id = ? AND anchor_schedule_entry_id = ?
				LIMIT 1`, channelID, target.ID).Scan(&successorID); txErr != nil && !errors.Is(txErr, sql.ErrNoRows) {
				return txErr
			}
		}

		if txErr := db.ShiftScheduleEntriesStartAtOrAfter(ctx, tx, channelID, insertionStartMs, durationMs); txErr != nil {
			return txErr
		}

		newID := uuid.NewString()
		anchor := target.AnchorScheduleEntryID
		if after {
			anchor = &target.ID
			if successorID.Valid {
				if _, txErr := tx.ExecContext(ctx,
					`UPDATE schedule_entries SET anchor_schedule_entry_id = ? WHERE id = ?`,
					newID, successorID.String,
				); txErr != nil {
					return txErr
				}
			}
		} else {
			if _, txErr := tx.ExecContext(ctx,
				`UPDATE schedule_entries SET anchor_schedule_entry_id = ? WHERE id = ?`,
				newID, target.ID,
			); txErr != nil {
				return txErr
			}
		}
		if _, txErr := tx.ExecContext(ctx, `
			INSERT INTO schedule_entries (
				id, channel_id, start_ms, media_id, offset_ms, duration_ms,
				anchor_schedule_entry_id, created_at_ms
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, channelID, insertionStartMs, mediaID, 0, durationMs, anchor, nowMs,
		); txErr != nil {
			return txErr
		}

		inserted = insertRelativeResult{
			EntryID:    newID,
			StartMs:    insertionStartMs,
			DurationMs: durationMs,
		}
		return nil
	})
	if err != nil {
		return insertRelativeResult{}, err
	}
	return inserted, nil
}

// RemoveMedia removes a media item from a channel's membership. When
// pruneSchedule is true it also clears any future schedule entries for that
// media and re-extends the schedule from the earliest pruned point.
// RebuildStartMs in the result is zero when no prune occurred.
func (s *scheduleService) RemoveMedia(ctx context.Context, channelID, mediaID string, pruneSchedule bool) (removeMediaResult, error) {
	ch, err := db.ChannelByID(ctx, s.db, channelID)
	if err != nil {
		return removeMediaResult{}, err
	}
	if ch == nil {
		return removeMediaResult{}, errChannelNotFound
	}

	isMember, err := db.ChannelMediaExists(ctx, s.db, channelID, mediaID)
	if err != nil {
		return removeMediaResult{}, err
	}
	if !isMember {
		return removeMediaResult{}, errMediaNotInChannel
	}

	if !pruneSchedule {
		err := db.WithTx(ctx, s.db, func(tx db.Execer) error {
			_, txErr := db.RemoveChannelMedia(ctx, tx, channelID, mediaID)
			return txErr
		})
		if err != nil {
			return removeMediaResult{}, err
		}
		return removeMediaResult{}, nil
	}

	nowMs := s.now().UTC().UnixMilli()
	rebuildStartMs, err := db.FirstScheduleEntryForMediaAtOrAfter(ctx, s.db, channelID, mediaID, nowMs)
	if err != nil {
		return removeMediaResult{}, err
	}

	if rebuildStartMs == 0 {
		// No future schedule entries for this media; remove membership only.
		err := db.WithTx(ctx, s.db, func(tx db.Execer) error {
			_, txErr := db.RemoveChannelMedia(ctx, tx, channelID, mediaID)
			return txErr
		})
		if err != nil {
			return removeMediaResult{}, err
		}
		return removeMediaResult{}, nil
	}

	res := removeMediaResult{RebuildStartMs: rebuildStartMs}
	var extResult scheduler.ExtendResult
	var extErr error

	err = db.WithImmediateTx(ctx, s.db, func(tx db.Execer) error {
		var txErr error
		res.Pruned, txErr = db.ClearScheduleAfter(ctx, tx, channelID, rebuildStartMs)
		if txErr != nil {
			return txErr
		}
		if _, txErr = db.RemoveChannelMedia(ctx, tx, channelID, mediaID); txErr != nil {
			return txErr
		}
		extResult, extErr = scheduler.ExtendChannel(ctx, tx, channelID, scheduler.ServiceOptions{
			HorizonHours:       24,
			InTransaction:      true,
			ResumeAfterMediaID: mediaID,
		})
		if extErr != nil && !errors.Is(extErr, scheduler.ErrNoReadyPackages) {
			return extErr
		}
		return nil
	})
	if err != nil {
		return removeMediaResult{}, err
	}

	res.Inserted = int64(extResult.Inserted)
	res.NoPackages = errors.Is(extErr, scheduler.ErrNoReadyPackages)
	return res, nil
}
