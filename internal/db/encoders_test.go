package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

const testProfile = "h264-1080p-8mbps"

// encoderTestEnv bundles a writable DB + a registered encoder + a media row +
// a channel and channel_media row, since every job-op test needs those. Tests
// that want a different policy override the channel's encoder_policy after
// calling this.
type encoderTestEnv struct {
	conn      *sql.DB
	encoderID string
	rawKey    string
}

func newEncoderTestEnv(t *testing.T) *encoderTestEnv {
	t.Helper()
	path := newTestDB(t)
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channels (id, display_name, source_directory, ordering, enabled, created_at_ms)
		VALUES ('ch1', 'Test', '/tmp', 'linear', 1, 0)`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm1', NULL, 0)`); err != nil {
		t.Fatalf("insert channel_media: %v", err)
	}

	id, raw, err := RegisterEncoder(ctx, conn, "test-encoder", `{"hwaccel":"nvenc"}`, 1_000)
	if err != nil {
		t.Fatalf("register encoder: %v", err)
	}
	// Register a local encoder so local claim tests have a valid concurrency
	// row to read. The default concurrency is 1 (from the encoders schema).
	if _, err := EnsureLocalEncoder(ctx, conn, "Local Worker", 1_000); err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}
	return &encoderTestEnv{conn: conn, encoderID: id, rawKey: raw}
}

func setChannelPolicy(t *testing.T, conn *sql.DB, channelID string, policy EncoderPolicy) {
	t.Helper()
	if _, err := conn.Exec(`UPDATE channels SET encoder_policy = ? WHERE id = ?`, string(policy), channelID); err != nil {
		t.Fatalf("set encoder_policy: %v", err)
	}
}

func mustClaim(t *testing.T, conn *sql.DB, req ClaimRequest) {
	t.Helper()
	ok, err := ClaimPackage(context.Background(), conn, req)
	if err != nil {
		t.Fatalf("claim err: %v", err)
	}
	if !ok {
		t.Fatalf("claim did not win")
	}
}

func TestRegisterEncoder_HashesAndReturnsKeyOnce(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()

	if !strings.HasPrefix(env.rawKey, apiKeyPrefix) {
		t.Fatalf("raw key missing prefix: %q", env.rawKey)
	}
	enc, err := GetEncoderByAPIKey(ctx, env.conn, env.rawKey)
	if err != nil || enc == nil {
		t.Fatalf("lookup by key: enc=%v err=%v", enc, err)
	}
	if enc.APIKeyHash == env.rawKey {
		t.Fatalf("api_key_hash equals raw key — hash was not applied")
	}
	if enc.APIKeyHash != hashAPIKey(env.rawKey) {
		t.Fatalf("api_key_hash mismatch")
	}
	if enc.Status != EncoderStatusPending || enc.IsRevoked() {
		t.Fatalf("fresh encoder should be pending and not revoked: %+v", enc)
	}
	if enc.LastSeenMs != 0 {
		t.Fatalf("fresh encoder should have last_seen_ms=0 until first ping: %d", enc.LastSeenMs)
	}

	// Wrong key returns nil.
	bogus, err := GetEncoderByAPIKey(ctx, env.conn, "lcenc_deadbeef")
	if err != nil {
		t.Fatalf("lookup bogus key err: %v", err)
	}
	if bogus != nil {
		t.Fatalf("bogus key matched an encoder: %+v", bogus)
	}
}

func TestRevokeEncoder_BlocksAllJobOps(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()

	// Claim before revoke so we have a lease to test against.
	mustClaim(t, env.conn, ClaimRequest{
		MediaID:   "m1",
		Profile:   testProfile,
		PackageID: "pkg-m1",
		EncoderID: env.encoderID,
		LeaseTTL:  10 * time.Second,
		NowMs:     1_000,
	})

	if err := RevokeEncoder(ctx, env.conn, env.encoderID, 2_000); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	enc, err := GetEncoderByID(ctx, env.conn, env.encoderID)
	if err != nil || enc == nil {
		t.Fatalf("get after revoke: %v", err)
	}
	if !enc.IsRevoked() || enc.Status != EncoderStatusOffline {
		t.Fatalf("revoke did not mark encoder: %+v", enc)
	}

	// Heartbeat rejected.
	if _, err := HeartbeatEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, 10*time.Second, nil, 3_000); err == nil {
		t.Fatalf("heartbeat should reject revoked encoder")
	}
	// Complete rejected.
	if err := CompleteEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, FinalizedPackage{}, 3_000); err == nil {
		t.Fatalf("complete should reject revoked encoder")
	}
	// Fail rejected.
	if _, err := FailEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, "transient", "test", 3, 3_000); err == nil {
		t.Fatalf("fail should reject revoked encoder")
	}
	// A second claim is also rejected (would need a different package row).
	if _, err := env.conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m2', '/tmp/m2.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("seed m2: %v", err)
	}
	ok, err := ClaimPackage(ctx, env.conn, ClaimRequest{
		MediaID: "m2", Profile: testProfile, PackageID: "pkg-m2",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 4_000,
	})
	if err == nil || ok {
		t.Fatalf("claim by revoked encoder should fail: ok=%v err=%v", ok, err)
	}
}

// TestEncoderJobOpErrorWireText pins the exact Error() text of the encoder
// job-op failures. These strings are returned verbatim to encoder HTTP clients
// (admin writeError uses err.Error()) and are switched on by classifyJobOpError,
// so they are a wire contract. Characterizes the text before sentinel errors are
// introduced; the assertions must remain byte-identical after that refactor.
func TestEncoderJobOpErrorWireText(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()

	// not registered: any job op against an unknown encoder.
	if err := RevokeEncoder(ctx, env.conn, "ghost-encoder", 1_000); err == nil {
		t.Fatal("expected error revoking missing encoder")
	} else if got := err.Error(); got != "encoder ghost-encoder not registered" {
		t.Fatalf("not-registered text changed: %q", got)
	} else if !errors.Is(err, ErrEncoderNotRegistered) {
		t.Fatalf("not-registered error not wrapped with ErrEncoderNotRegistered: %v", err)
	}

	// Claim pkg-m1 with the registered encoder so it holds a processing lease.
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})

	// no active lease: heartbeat a package that has no job row.
	if _, err := HeartbeatEncoderJob(ctx, env.conn, "pkg-missing", env.encoderID, 10*time.Second, nil, 2_000); err == nil {
		t.Fatal("expected no-active-lease error")
	} else if got := err.Error(); got != "no active lease for package pkg-missing" {
		t.Fatalf("no-active-lease text changed: %q", got)
	} else if !errors.Is(err, ErrNoActiveLease) {
		t.Fatalf("no-active-lease error not wrapped with ErrNoActiveLease: %v", err)
	}

	// leased by encoder: a different encoder touches pkg-m1.
	otherID, _, err := RegisterEncoder(ctx, env.conn, "other", `{}`, 1_000)
	if err != nil {
		t.Fatalf("register other encoder: %v", err)
	}
	if _, err := HeartbeatEncoderJob(ctx, env.conn, "pkg-m1", otherID, 10*time.Second, nil, 2_000); err == nil {
		t.Fatal("expected leased-by-encoder error")
	} else if got, want := err.Error(), "package pkg-m1 is leased by encoder "+env.encoderID+", not "+otherID; got != want {
		t.Fatalf("leased-by-encoder text changed:\n got %q\nwant %q", got, want)
	} else if !errors.Is(err, ErrPackageLeasedByOther) {
		t.Fatalf("leased-by-encoder error not wrapped with ErrPackageLeasedByOther: %v", err)
	}

	// not processing: force the package out of processing, then fail it.
	if _, err := env.conn.Exec(`UPDATE media_packages SET status = 'ready' WHERE id = 'pkg-m1'`); err != nil {
		t.Fatalf("force ready: %v", err)
	}
	if _, err := FailEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, "transient", "x", 3, 2_000); err == nil {
		t.Fatal("expected not-processing error")
	} else if got := err.Error(); got != "package pkg-m1 is not processing" {
		t.Fatalf("not-processing text changed: %q", got)
	} else if !errors.Is(err, ErrPackageNotProcessing) {
		t.Fatalf("not-processing error not wrapped with ErrPackageNotProcessing: %v", err)
	}

	// is revoked: revoke the lease holder, then have it touch its own package.
	if _, err := env.conn.Exec(`UPDATE media_packages SET status = 'processing' WHERE id = 'pkg-m1'`); err != nil {
		t.Fatalf("restore processing: %v", err)
	}
	if err := RevokeEncoder(ctx, env.conn, env.encoderID, 3_000); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := HeartbeatEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, 10*time.Second, nil, 4_000); err == nil {
		t.Fatal("expected revoked error")
	} else if got := err.Error(); got != "encoder "+env.encoderID+" is revoked" {
		t.Fatalf("revoked text changed: %q", got)
	} else if !errors.Is(err, ErrEncoderRevoked) {
		t.Fatalf("revoked error not wrapped with ErrEncoderRevoked: %v", err)
	}
}

func TestRevokeEncoder_IdempotentAndMissing(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	if err := RevokeEncoder(ctx, env.conn, env.encoderID, 1_000); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := RevokeEncoder(ctx, env.conn, env.encoderID, 2_000); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	if err := RevokeEncoder(ctx, env.conn, "enc_nonexistent", 3_000); err == nil {
		t.Fatalf("revoke of missing encoder should error")
	}
}

func TestClaimPackage_RemoteCreatesLease(t *testing.T) {
	env := newEncoderTestEnv(t)

	mustClaim(t, env.conn, ClaimRequest{
		MediaID:   "m1",
		Profile:   testProfile,
		PackageID: "pkg-m1",
		EncoderID: env.encoderID,
		LeaseTTL:  10 * time.Second,
		NowMs:     5_000,
	})

	var encoderID string
	var leaseExpiresMs, lastHeartbeatMs int64
	err := env.conn.QueryRow(`SELECT encoder_id, lease_expires_ms, last_heartbeat_ms FROM encoder_jobs WHERE package_id = 'pkg-m1'`).
		Scan(&encoderID, &leaseExpiresMs, &lastHeartbeatMs)
	if err != nil {
		t.Fatalf("lookup lease: %v", err)
	}
	if encoderID != env.encoderID || leaseExpiresMs != 15_000 || lastHeartbeatMs != 5_000 {
		t.Fatalf("lease mismatch: enc=%s expires=%d heartbeat=%d", encoderID, leaseExpiresMs, lastHeartbeatMs)
	}

	pkg, err := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup pkg: %v", err)
	}
	if pkg.Attempts != 1 {
		t.Fatalf("attempts=%d, want 1 on fresh claim", pkg.Attempts)
	}
}

func TestClaimPackage_LocalOnRemoteOnlyPolicyRejected(t *testing.T) {
	env := newEncoderTestEnv(t)
	setChannelPolicy(t, env.conn, "ch1", EncoderPolicyRemoteOnly)
	ok, err := ClaimPackage(context.Background(), env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1", NowMs: 1_000,
	})
	if err != nil {
		t.Fatalf("policy rejection should be (false, nil), got err=%v", err)
	}
	if ok {
		t.Fatalf("local claim on remote_only should not succeed")
	}
	// The row was never created — policy check runs before any state write.
	pkg, _ := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if pkg != nil {
		t.Fatalf("rejected claim should not create a package row, got %+v", pkg)
	}
}

func TestClaimPackage_RemoteOnLocalOnlyPolicyRejected(t *testing.T) {
	env := newEncoderTestEnv(t)
	setChannelPolicy(t, env.conn, "ch1", EncoderPolicyLocalOnly)
	ok, err := ClaimPackage(context.Background(), env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	if err != nil {
		t.Fatalf("policy rejection should be (false, nil), got err=%v", err)
	}
	if ok {
		t.Fatalf("remote claim on local_only should not succeed")
	}
	// Lease row should not exist either.
	var n int
	if err := env.conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs WHERE package_id = 'pkg-m1'`).Scan(&n); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if n != 0 {
		t.Fatalf("rejected remote claim should not insert lease row, got %d", n)
	}
}

// TestLocalEncoderClaim_* tests verify that the local claim path reads
// concurrency from the encoder row rather than a special settings key.

func TestLocalEncoderClaim_NoLocalEncoderRegistered(t *testing.T) {
	// Fresh DB with no local encoder in settings — local claim must not succeed.
	path := newTestDB(t)
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	ok, err := ClaimPackage(ctx, conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1", NowMs: 1_000,
	})
	if err != nil {
		t.Fatalf("unregistered local encoder should return (false, nil), got err=%v", err)
	}
	if ok {
		t.Fatalf("local claim without registered encoder should not succeed")
	}
}

func TestLocalEncoderClaim_ConcurrencyZeroBlocksClaim(t *testing.T) {
	path := newTestDB(t)
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	localID, err := EnsureLocalEncoder(ctx, conn, "Local Worker", 1_000)
	if err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}
	if err := UpdateEncoderConcurrency(ctx, conn, localID, 0); err != nil {
		t.Fatalf("set concurrency 0: %v", err)
	}

	ok, err := ClaimPackage(ctx, conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1", NowMs: 1_000,
	})
	if err != nil {
		t.Fatalf("concurrency=0 should return (false, nil), got err=%v", err)
	}
	if ok {
		t.Fatalf("local claim with concurrency=0 should not succeed")
	}
}

func TestLocalEncoderClaim_ConcurrencyOneAllowsClaim(t *testing.T) {
	path := newTestDB(t)
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	if _, err := EnsureLocalEncoder(ctx, conn, "Local Worker", 1_000); err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}
	// Default concurrency after EnsureLocalEncoder is 1 (schema default).

	ok, err := ClaimPackage(ctx, conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1", NowMs: 1_000,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatalf("local claim with concurrency=1 should succeed")
	}
}

func TestLocalEncoderClaim_RevokedLocalEncoderBlocksClaim(t *testing.T) {
	path := newTestDB(t)
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := context.Background()

	if _, err := conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m1', '/tmp/m1.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	localID, err := EnsureLocalEncoder(ctx, conn, "Local Worker", 1_000)
	if err != nil {
		t.Fatalf("ensure local encoder: %v", err)
	}
	if err := RevokeEncoder(ctx, conn, localID, 2_000); err != nil {
		t.Fatalf("revoke local encoder: %v", err)
	}

	ok, err := ClaimPackage(ctx, conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1", NowMs: 3_000,
	})
	if err != nil {
		t.Fatalf("revoked local encoder should return (false, nil), got err=%v", err)
	}
	if ok {
		t.Fatalf("local claim with revoked encoder should not succeed")
	}
}

func TestClaimPackage_DuplicateClaimReturnsFalse(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	ok, err := ClaimPackage(ctx, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 2_000,
	})
	if err != nil {
		t.Fatalf("duplicate claim err: %v", err)
	}
	if ok {
		t.Fatalf("duplicate claim should return false, got true")
	}
}

func TestHeartbeatEncoderJob_WrongEncoderRejected(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	otherID, _, err := RegisterEncoder(ctx, env.conn, "other", "{}", 1_000)
	if err != nil {
		t.Fatalf("register other encoder: %v", err)
	}
	if _, err := HeartbeatEncoderJob(ctx, env.conn, "pkg-m1", otherID, 10*time.Second, nil, 2_000); err == nil {
		t.Fatalf("heartbeat by wrong encoder should fail")
	}

	// Owner heartbeat extends the lease.
	progress := 42
	newLease, err := HeartbeatEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, 30*time.Second, &progress, 5_000)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if newLease != 35_000 {
		t.Fatalf("new lease=%d want 35000", newLease)
	}
	var leaseExpires int64
	var progressPct sql.NullInt64
	if err := env.conn.QueryRow(`SELECT lease_expires_ms, progress_pct FROM encoder_jobs WHERE package_id = 'pkg-m1'`).
		Scan(&leaseExpires, &progressPct); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if leaseExpires != 35_000 || !progressPct.Valid || progressPct.Int64 != 42 {
		t.Fatalf("lease/progress mismatch: expires=%d progress=%+v", leaseExpires, progressPct)
	}
}

func TestCompleteEncoderJob_WrongEncoderRejected(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	otherID, _, _ := RegisterEncoder(ctx, env.conn, "other", "{}", 1_000)
	if err := CompleteEncoderJob(ctx, env.conn, "pkg-m1", otherID, FinalizedPackage{}, 2_000); err == nil {
		t.Fatalf("complete by wrong encoder should fail")
	}
}

func TestFailEncoderJob_WrongEncoderRejected(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	otherID, _, _ := RegisterEncoder(ctx, env.conn, "other", "{}", 1_000)
	if _, err := FailEncoderJob(ctx, env.conn, "pkg-m1", otherID, "transient", "test", 3, 2_000); err == nil {
		t.Fatalf("fail by wrong encoder should fail")
	}
}

func TestLeaseExpiry_ResetsToPendingWhenAttemptsRemain(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 5 * time.Second, NowMs: 1_000,
	})
	// Lease expires at 6_000. Sweep at 7_000 with cap=3, attempts=1.
	results, err := LeaseExpiredJobs(ctx, env.conn, 7_000, 3)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(results) != 1 || results[0].PackageID != "pkg-m1" || results[0].NewStatus != PackageStatusPending {
		t.Fatalf("expected one pending result, got %+v", results)
	}

	pkg, err := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup pkg: %v", err)
	}
	if pkg.Status != PackageStatusPending {
		t.Fatalf("status=%s, want pending", pkg.Status)
	}
	if pkg.LastAttemptError == nil || !strings.Contains(*pkg.LastAttemptError, "lease expired") {
		t.Fatalf("last_attempt_error=%+v, want 'lease expired'", pkg.LastAttemptError)
	}
	if pkg.Error != nil {
		t.Fatalf("error should be NULL on transient failure: %+v", pkg.Error)
	}
	if pkg.Attempts != 1 {
		t.Fatalf("attempts=%d, want unchanged (1)", pkg.Attempts)
	}

	// Lease row gone.
	var leaseRows int
	if err := env.conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs WHERE package_id = 'pkg-m1'`).Scan(&leaseRows); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leaseRows != 0 {
		t.Fatalf("expired lease row not deleted")
	}
}

func TestLeaseExpiry_ToFailedWhenAttemptsExhausted(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	cap := 2

	for i := 0; i < cap; i++ {
		nowMs := int64(1_000 * (i + 1))
		mustClaim(t, env.conn, ClaimRequest{
			MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
			EncoderID: env.encoderID, LeaseTTL: 100 * time.Millisecond, NowMs: nowMs,
		})
		// Sweep at nowMs + 200ms to expire the lease.
		_, err := LeaseExpiredJobs(ctx, env.conn, nowMs+200, cap)
		if err != nil {
			t.Fatalf("sweep iter=%d: %v", i, err)
		}
	}

	pkg, err := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup pkg: %v", err)
	}
	if pkg.Status != PackageStatusFailed {
		t.Fatalf("status=%s, want failed after cap exhausted", pkg.Status)
	}
	if pkg.Error == nil {
		t.Fatalf("terminal failure should set error: %+v", pkg.Error)
	}
	if pkg.Attempts != int64(cap) {
		t.Fatalf("attempts=%d, want %d", pkg.Attempts, cap)
	}
}

func TestCompleteAfterLeaseExpiryRejected(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 1 * time.Second, NowMs: 1_000,
	})
	// Sweep expires the lease and resets the package to pending.
	if _, err := LeaseExpiredJobs(ctx, env.conn, 5_000, 3); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	// The encoder, unaware of the sweep, tries to complete.
	err := CompleteEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, FinalizedPackage{}, 6_000)
	if err == nil {
		t.Fatalf("complete after lease expiry should fail")
	}
	if !strings.Contains(err.Error(), "no active lease") && !strings.Contains(err.Error(), "not processing") {
		t.Fatalf("expected lease/processing error, got %v", err)
	}
}

func TestFailEncoderJob_TerminalGoesStraightToFailed(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	// Cap is high, so transient would normally retry — but terminal short-circuits.
	status, err := FailEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, "terminal", "source missing", 100, 2_000)
	if err != nil {
		t.Fatalf("fail terminal: %v", err)
	}
	if status != PackageStatusFailed {
		t.Fatalf("status=%s, want failed", status)
	}
	pkg, _ := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if pkg.Error == nil || *pkg.Error != "source missing" {
		t.Fatalf("error=%+v, want 'source missing'", pkg.Error)
	}
}

func TestFailEncoderJob_TransientReturnsPending(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	status, err := FailEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, "transient", "ffmpeg crashed", 3, 2_000)
	if err != nil {
		t.Fatalf("fail transient: %v", err)
	}
	if status != PackageStatusPending {
		t.Fatalf("status=%s, want pending", status)
	}
	pkg, _ := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if pkg.LastAttemptError == nil || *pkg.LastAttemptError != "ffmpeg crashed" {
		t.Fatalf("last_attempt_error=%+v, want 'ffmpeg crashed'", pkg.LastAttemptError)
	}
	if pkg.Error != nil {
		t.Fatalf("error should be NULL on transient fail")
	}
}

func TestClaimAfterTransientFail_IncrementsAttempts(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	if _, err := FailEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, "transient", "first", 3, 2_000); err != nil {
		t.Fatalf("fail: %v", err)
	}
	// Re-claim. attempts should go from 1 to 2.
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 3_000,
	})
	pkg, _ := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if pkg.Attempts != 2 {
		t.Fatalf("attempts=%d, want 2", pkg.Attempts)
	}
	if pkg.LastAttemptError != nil {
		t.Fatalf("last_attempt_error should be cleared on re-claim: %+v", pkg.LastAttemptError)
	}
}

func TestOperatorRetryResetsAttempts(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	if _, err := FailEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, "terminal", "fatal", 3, 2_000); err != nil {
		t.Fatalf("fail terminal: %v", err)
	}
	// Operator retry path.
	res, err := RequestMediaPackages(context.Background(), env.conn, []string{"m1"}, testProfile)
	if err != nil {
		t.Fatalf("request retry: %v", err)
	}
	if len(res.Queued) != 1 {
		t.Fatalf("expected one requeued, got %+v", res)
	}
	pkg, _ := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if pkg.Attempts != 0 {
		t.Fatalf("attempts=%d, want 0 after operator retry", pkg.Attempts)
	}
}

func TestDeleteEncoder_ReleasesActiveLease(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	if err := DeleteEncoder(ctx, env.conn, env.encoderID, 2_000); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Encoder row gone.
	enc, err := GetEncoderByID(ctx, env.conn, env.encoderID)
	if err != nil {
		t.Fatalf("lookup encoder: %v", err)
	}
	if enc != nil {
		t.Fatalf("encoder row not deleted: %+v", enc)
	}
	// Lease row gone.
	var leaseRows int
	if err := env.conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs WHERE encoder_id = ?`, env.encoderID).Scan(&leaseRows); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leaseRows != 0 {
		t.Fatalf("lease rows remain: %d", leaseRows)
	}
	// Package released back to pending so another claim can pick it up.
	pkg, err := MediaPackageByID(context.Background(), env.conn, "pkg-m1")
	if err != nil || pkg == nil {
		t.Fatalf("lookup pkg: %v", err)
	}
	if pkg.Status != PackageStatusPending {
		t.Fatalf("status=%s, want pending after encoder delete", pkg.Status)
	}
	if pkg.LastAttemptError == nil || !strings.Contains(*pkg.LastAttemptError, "encoder deleted") {
		t.Fatalf("last_attempt_error=%+v, want 'encoder deleted'", pkg.LastAttemptError)
	}
}

func TestDeleteEncoder_NoActiveLease(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	if err := DeleteEncoder(ctx, env.conn, env.encoderID, 1_000); err != nil {
		t.Fatalf("delete: %v", err)
	}
	enc, _ := GetEncoderByID(ctx, env.conn, env.encoderID)
	if enc != nil {
		t.Fatalf("encoder row not deleted: %+v", enc)
	}
}

func TestDeleteEncoder_UnknownID(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	err := DeleteEncoder(ctx, env.conn, "enc_does_not_exist", 1_000)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected not-registered error, got %v", err)
	}
}

func TestListEncoderJobSummaries(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()

	// No jobs yet — should return empty.
	sums, err := ListEncoderJobSummaries(ctx, env.conn)
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(sums) != 0 {
		t.Fatalf("expected 0 jobs, got %d", len(sums))
	}

	// Claim a package so a job row appears.
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	progress := 42
	_, err = HeartbeatEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, 10*time.Second, &progress, 2_000)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	sums, err = ListEncoderJobSummaries(ctx, env.conn)
	if err != nil {
		t.Fatalf("list after claim: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("expected 1 job, got %d", len(sums))
	}
	j := sums[0]
	if j.EncoderID != env.encoderID {
		t.Fatalf("encoderID=%s, want %s", j.EncoderID, env.encoderID)
	}
	if j.PackageID != "pkg-m1" {
		t.Fatalf("packageID=%s, want pkg-m1", j.PackageID)
	}
	if j.MediaID != "m1" {
		t.Fatalf("mediaID=%s, want m1", j.MediaID)
	}
	if j.Profile != testProfile {
		t.Fatalf("profile=%s, want %s", j.Profile, testProfile)
	}
	if j.ProgressPct == nil || *j.ProgressPct != 42 {
		t.Fatalf("progress=%v, want 42", j.ProgressPct)
	}
	if j.LeaseExpiresMs != 12_000 {
		t.Fatalf("leaseExpiresMs=%d, want 12000", j.LeaseExpiresMs)
	}
	if j.ClaimedAtMs != 1_000 {
		t.Fatalf("claimedAtMs=%d, want 1000", j.ClaimedAtMs)
	}

	// Complete the job — summary should empty again.
	if err := CompleteEncoderJob(ctx, env.conn, "pkg-m1", env.encoderID, FinalizedPackage{}, 3_000); err != nil {
		t.Fatalf("complete: %v", err)
	}
	sums, err = ListEncoderJobSummaries(ctx, env.conn)
	if err != nil {
		t.Fatalf("list after complete: %v", err)
	}
	if len(sums) != 0 {
		t.Fatalf("expected 0 jobs after complete, got %d", len(sums))
	}
}

// The local worker now claims as its registered encoder so the sweeper can
// supervise it, but it must still read as local for channel encoder_policy.
func TestClaimPackage_LocalWorkerLeasedButPolicyLocal(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	localID := GetLocalEncoderID(ctx, env.conn)
	if localID == "" {
		t.Fatal("local encoder not registered by test env")
	}
	// A local_only channel must accept the local worker even though it now
	// carries an encoder identity and holds a lease.
	setChannelPolicy(t, env.conn, "ch1", EncoderPolicyLocalOnly)
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: localID, Local: true, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	var n int
	if err := env.conn.QueryRow(
		`SELECT COUNT(*) FROM encoder_jobs WHERE package_id = 'pkg-m1' AND encoder_id = ?`,
		localID).Scan(&n); err != nil {
		t.Fatalf("count lease: %v", err)
	}
	if n != 1 {
		t.Fatalf("local claim should insert exactly one lease, got %d", n)
	}
}

func TestClaimPackage_LocalWorkerBlockedByRemoteOnly(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	localID := GetLocalEncoderID(ctx, env.conn)
	setChannelPolicy(t, env.conn, "ch1", EncoderPolicyRemoteOnly)
	ok, err := ClaimPackage(ctx, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: localID, Local: true, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	if err != nil {
		t.Fatalf("claim err: %v", err)
	}
	if ok {
		t.Fatal("remote_only channel must reject the local worker")
	}
	var n int
	env.conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs`).Scan(&n)
	if n != 0 {
		t.Fatalf("rejected claim left %d lease rows", n)
	}
}

// Local concurrency is now counted from leases (encoder_jobs), not from
// lease-free processing rows.
func TestClaimPackage_LocalConcurrencyCountsLeases(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	localID := GetLocalEncoderID(ctx, env.conn)
	// Default local concurrency is 1; the first claim holds the only slot.
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: localID, Local: true, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	if _, err := env.conn.Exec(`INSERT INTO media (id, path, directory, duration_ms, container,
		video_codec, video_height, audio_codec, codec_check_passed, ingested_at_ms)
		VALUES ('m2', '/tmp/m2.mkv', '/tmp', 12000, 'mkv', 'h264', 1080, 'aac', 1, 0)`); err != nil {
		t.Fatalf("seed m2: %v", err)
	}
	if _, err := env.conn.Exec(`INSERT INTO channel_media (channel_id, media_id, anchor_media_id, added_at_ms)
		VALUES ('ch1', 'm2', 'm1', 0)`); err != nil {
		t.Fatalf("link m2: %v", err)
	}
	// Cap=1 and one lease is held, so the second claim must be refused.
	ok, err := ClaimPackage(ctx, env.conn, ClaimRequest{
		MediaID: "m2", Profile: testProfile, PackageID: "pkg-m2",
		EncoderID: localID, Local: true, LeaseTTL: 10 * time.Second, NowMs: 2_000,
	})
	if err != nil {
		t.Fatalf("claim err: %v", err)
	}
	if ok {
		t.Fatal("second local claim should be blocked by concurrency=1")
	}
	// Raise the cap; the second claim now wins and adds a second lease.
	if err := UpdateEncoderConcurrency(ctx, env.conn, localID, 2); err != nil {
		t.Fatalf("bump concurrency: %v", err)
	}
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m2", Profile: testProfile, PackageID: "pkg-m2",
		EncoderID: localID, Local: true, LeaseTTL: 10 * time.Second, NowMs: 3_000,
	})
}

// RequeueEncoderJobs is the worker's startup self-recovery: it force-requeues
// the encoder's own leftover leases without waiting for expiry, because a
// freshly-started worker owns no live jobs.
func TestRequeueEncoderJobs_RecoversOwnLeasesBeforeExpiry(t *testing.T) {
	env := newEncoderTestEnv(t)
	ctx := context.Background()
	mustClaim(t, env.conn, ClaimRequest{
		MediaID: "m1", Profile: testProfile, PackageID: "pkg-m1",
		EncoderID: env.encoderID, LeaseTTL: 10 * time.Second, NowMs: 1_000,
	})
	// now=2_000 is well inside the 10s lease, yet the leftover lease is requeued.
	results, err := RequeueEncoderJobs(ctx, env.conn, env.encoderID, 5, 2_000)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if len(results) != 1 || results[0].NewStatus != PackageStatusPending {
		t.Fatalf("want one pending requeue, got %+v", results)
	}
	var status string
	if err := env.conn.QueryRow(`SELECT status FROM media_packages WHERE id='pkg-m1'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if PackageStatus(status) != PackageStatusPending {
		t.Fatalf("status=%s, want pending", status)
	}
	var n int
	env.conn.QueryRow(`SELECT COUNT(*) FROM encoder_jobs WHERE package_id='pkg-m1'`).Scan(&n)
	if n != 0 {
		t.Fatalf("lease not cleared, %d remain", n)
	}
}

