package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingRetentionPreservesLiveRecentAndHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.RecordingRetentionDays != 30 {
		t.Fatalf("default = %d", settings.RecordingRetentionDays)
	}
	settings.RecordingRetentionDays = 7
	if err = s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	// Old callers omitting the new field must not reset an existing policy.
	settings.RecordingRetentionDays = 0
	if err = s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, _ = s.Settings(ctx)
	if settings.RecordingRetentionDays != 7 {
		t.Fatal("legacy save reset retention")
	}
	old := time.Now().UTC().AddDate(0, 0, -8).Format(time.RFC3339)
	recent := time.Now().UTC().AddDate(0, 0, -6).Format(time.RFC3339)
	for _, fixture := range []struct {
		id, kind string
		ended    any
	}{{"old", "terminal", old}, {"recent", "terminal", recent}, {"live", "terminal", nil}, {"other", "db", old}} {
		_, err = s.db.Exec(`INSERT INTO sessions(id,actor,kind,started_at,ended_at) VALUES(?,'test',?,?,?)`, fixture.id, fixture.kind, old, fixture.ended)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecordAccessOutput(ctx, fixture.id, 0, []byte("retention test")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_recording_delete BEFORE DELETE ON access_recordings BEGIN SELECT RAISE(ABORT,'fixture delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CleanupRecordings(ctx); err == nil {
		t.Fatal("cleanup ignored delete failure")
	}
	var marked int
	if err = s.db.QueryRow(`SELECT count(*) FROM sessions WHERE recording_purged_at<>''`).Scan(&marked); err != nil || marked != 0 {
		t.Fatalf("failed cleanup left expiry markers: %d %v", marked, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_recording_delete`); err != nil {
		t.Fatal(err)
	}
	count, err := s.CleanupRecordings(ctx)
	if err != nil || count != 1 {
		t.Fatalf("cleanup = %d, %v", count, err)
	}
	for _, id := range []string{"old", "recent", "live", "other"} {
		var purged string
		var rows int
		if err = s.db.QueryRow(`SELECT recording_purged_at FROM sessions WHERE id=?`, id).Scan(&purged); err != nil {
			t.Fatal("history removed", err)
		}
		if err = s.db.QueryRow(`SELECT count(*) FROM access_recordings WHERE session_id=?`, id).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if id == "old" {
			if purged == "" || rows != 0 {
				t.Fatal("expired payload survived")
			}
		} else if purged != "" || rows != 1 {
			t.Fatalf("protected session %s changed", id)
		}
	}
	if count, err = s.CleanupRecordings(ctx); err != nil || count != 0 {
		t.Fatalf("repeat = %d, %v", count, err)
	}
}
