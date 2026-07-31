package playback

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tckrcr/linearcast/internal/db"
	"github.com/tckrcr/linearcast/internal/ondemand"
)

func TestNewRuntimeLoadsEnabledChannelSnapshots(t *testing.T) {
	conn := newPlaybackTestDB(t)
	if _, err := conn.Exec(`INSERT INTO channels (
		id, display_name, source_directory, ordering, enabled, created_at_ms,
		required_package_profile, prefill_mode
	) VALUES ('ch', 'Channel', '/tmp', 'alphabetical', 1, 0, ?, 'on_demand')`,
		db.DefaultPackageProfile,
	); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	manager, err := ondemand.NewManager(ondemand.ManagerOptions{
		Root: filepath.Join(t.TempDir(), "encodings"),
		DB:   conn,
	})
	if err != nil {
		t.Fatalf("new on-demand manager: %v", err)
	}
	t.Cleanup(manager.Shutdown)

	runtime, err := NewRuntime(context.Background(), Config{
		DB:                    conn,
		Encodings:             manager,
		PackagedProfile:       db.DefaultPackageProfile,
		OnDemandPlaybackLagMs: DefaultOnDemandPlaybackLagMs,
		OnDemandWarmupMs:      DefaultOnDemandWarmupMs,
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}

	snapshots := runtime.ChannelSnapshots()
	if len(snapshots) != 1 {
		t.Fatalf("channel snapshots=%v, want one", snapshots)
	}
	if snapshots[0].ID != "ch" || snapshots[0].RequiredPackageProfile != db.DefaultPackageProfile {
		t.Fatalf("channel snapshot=%+v", snapshots[0])
	}
}

func TestNewRuntimeRequiresCoreDependencies(t *testing.T) {
	if _, err := NewRuntime(context.Background(), Config{}); err == nil {
		t.Fatal("NewRuntime succeeded without a database")
	}

	conn := newPlaybackTestDB(t)
	if _, err := NewRuntime(context.Background(), Config{DB: conn}); err == nil {
		t.Fatal("NewRuntime succeeded without an on-demand manager")
	}
}
