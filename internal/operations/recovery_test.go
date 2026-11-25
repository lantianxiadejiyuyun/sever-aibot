package operations

import (
	"context"
	"testing"
	"time"
)

func TestExpiredBatchRecoveryCountsMissingChecks(t *testing.T) {
	svc, c := fixture(t)
	ctx := context.Background()
	b, err := svc.Store.CreateInspectionBatch(ctx, "interrupted", []string{c.ID, "deleted-check"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.DB().Exec(`UPDATE inspection_batches SET created_at=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), b.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Store.RecoverInspectionBatches(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Store.GetInspectionBatch(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "interrupted" || got.Score != nil || got.Stale != 2 || len(got.Failures) != 2 {
		t.Fatalf("missing results counted as healthy: %+v", got)
	}
}
