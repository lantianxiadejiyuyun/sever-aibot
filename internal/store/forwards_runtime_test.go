package store

import (
	"context"
	"errors"
	"path/filepath"
	"server-aibot/internal/model"
	"testing"
	"time"
)

func TestForwardRevisionAndTraffic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, _, err := s.CreateNode(ctx, model.Node{Name: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.CreateForward(ctx, model.Forward{Name: "test", Kind: "port", NodeID: n.ID})
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.CommandForward(ctx, f.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	staleEdit := *started
	stopped, err := s.CommandForward(ctx, f.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateForwardRuntime(ctx, staleEdit); !errors.Is(err, ErrForwardConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	if err = s.DeleteInactiveForward(ctx, f.ID, started.Revision); !errors.Is(err, ErrForwardConflict) {
		t.Fatalf("delete bypassed revoke: %v", err)
	}
	if err = s.AckForward(ctx, n.ID, f.ID, started.Revision, "applied", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetForward(ctx, f.ID)
	if got.State != "stopping" {
		t.Fatalf("stale bind resurrected rule: %s", got.State)
	}
	_ = s.AckForward(ctx, "other", f.ID, stopped.Revision, "stopped", "")
	got, _ = s.GetForward(ctx, f.ID)
	if got.State != "stopping" {
		t.Fatal("cross node acknowledgement accepted")
	}
	_ = s.AckForward(ctx, n.ID, f.ID, stopped.Revision, "stopped", "")
	_ = s.AckForward(ctx, n.ID, f.ID, stopped.Revision, "applied", "")
	got, _ = s.GetForward(ctx, f.ID)
	if got.State != "stopped" {
		t.Fatal("bind overwrote stopped state")
	}
	frame := model.AgentFrame{ForwardID: f.ID, Revision: started.Revision, Ref: "report-1", UploadBytes: 37, DownloadBytes: 91, TS: time.Now().Unix()}
	if err = s.RecordForwardTraffic(ctx, "other", frame); err == nil {
		t.Fatal("accepted foreign traffic")
	}
	for i := 0; i < 2; i++ {
		if err = s.RecordForwardTraffic(ctx, n.ID, frame); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = s.GetForward(ctx, f.ID)
	if got.State != "stopped" || got.TrafficBytes != 128 {
		t.Fatalf("traffic replay/state: %+v", got)
	}
	days, err := s.ForwardTrafficDays(ctx, f.ID, 30)
	if err != nil || len(days) != 1 || days[0].UploadBytes != 37 || days[0].DownloadBytes != 91 {
		t.Fatalf("daily totals: %+v %v", days, err)
	}
	started, _ = s.CommandForward(ctx, f.ID, true)
	frame.Ref = "report-2"
	frame.Revision = started.Revision
	if err = s.RecordForwardTraffic(ctx, n.ID, frame); err != nil {
		t.Fatal(err)
	}
	_ = s.AckForward(ctx, n.ID, f.ID, started.Revision, "applied", "")
	got, _ = s.GetForward(ctx, f.ID)
	if got.State != "observed" {
		t.Fatal("late bind downgraded observed state")
	}
}
