package store

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
)

func TestChangeApprovalClaimAndReplay(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	a, err := st.CreateAsset(ctx, Asset{Name: "change fixture", Address: "127.0.0.1", Port: 22, Protocol: "ssh", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateChangeProposal(ctx, ChangeProposal{AssetID: a.ID, Kind: "service_restart", Service: "nginx", Summary: "restart", Actor: "admin", Revision: "snapshot", Plan: json.RawMessage(`{}`), Risk: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimChange(ctx, p.ID, p.Revision, "admin"); err == nil {
		t.Fatal("unapproved change claimed")
	}
	if err = st.ApproveChange(ctx, p.ID, "wrong", "admin"); err == nil {
		t.Fatal("wrong revision approved")
	}
	if err = st.ApproveChange(ctx, p.ID, p.Revision, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimChange(ctx, p.ID, p.Revision, "other"); err == nil {
		t.Fatal("other approver claimed")
	}
	var claims atomic.Int64
	var wg sync.WaitGroup
	var winner *ChangeExecution
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := st.ClaimChange(ctx, p.ID, p.Revision, "admin")
			if err == nil {
				claims.Add(1)
				mu.Lock()
				winner = run
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
	winner.Status = "rolled_back"
	winner.Output = "configuration restored"
	if err = st.FinishChange(ctx, *winner); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimChange(ctx, p.ID, p.Revision, "admin"); err == nil {
		t.Fatal("executed approval replayed")
	}
	saved, _ := st.GetChangeProposal(ctx, p.ID)
	if saved.Status != "rolled_back" {
		t.Fatalf("status=%s", saved.Status)
	}
	expired, err := st.CreateChangeProposal(ctx, ChangeProposal{AssetID: a.ID, Kind: p.Kind, Service: p.Service, Summary: p.Summary, Actor: "admin", Revision: "expired", Plan: p.Plan, Risk: p.Risk})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ApproveChange(ctx, expired.ID, expired.Revision, "admin"); err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`UPDATE change_proposals SET approved_until='2000-01-01T00:00:00Z' WHERE id=?`, expired.ID)
	if _, err = st.ClaimChange(ctx, expired.ID, expired.Revision, "admin"); err == nil {
		t.Fatal("expired approval replayed")
	}
}
