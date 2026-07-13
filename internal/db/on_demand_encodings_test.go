package db

import (
	"context"
	"testing"
)

func TestOnDemandEncodingLifecycleRows(t *testing.T) {
	path := newTestDB(t)
	conn, err := OpenReadWrite(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()
	ctx := context.Background()

	row := OnDemandEncoding{
		EncodingID:      "sess1",
		ChannelID:       "ch1",
		ScheduleEntryID: "entry1",
		MediaID:         "media1",
		Profile:         "h264-1080p-8mbps",
		State:           "starting",
		ProcessRunning:  true,
		SpawnedAtMs:     1000,
		LastProgressMs:  1000,
		SegmentCount:    0,
		UpdatedAtMs:     1000,
	}
	if err := UpsertOnDemandEncoding(ctx, conn, row); err != nil {
		t.Fatalf("insert encoding: %v", err)
	}

	row.State = "serving"
	row.FirstSegmentAtMs = 1300
	row.LastProgressMs = 1600
	row.SegmentCount = 2
	row.UpdatedAtMs = 1600
	if err := UpsertOnDemandEncoding(ctx, conn, row); err != nil {
		t.Fatalf("update encoding: %v", err)
	}

	got, err := ListOnDemandEncodings(ctx, conn)
	if err != nil {
		t.Fatalf("list encodings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].State != "serving" || !got[0].ProcessRunning || got[0].SegmentCount != 2 {
		t.Fatalf("unexpected encoding row: %+v", got[0])
	}

	if err := DeleteOnDemandEncoding(ctx, conn, "sess1"); err != nil {
		t.Fatalf("delete encoding: %v", err)
	}
	got, err = ListOnDemandEncodings(ctx, conn)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows after delete, want 0", len(got))
	}
}
