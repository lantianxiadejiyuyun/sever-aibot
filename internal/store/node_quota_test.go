package store

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"server-aibot/internal/model"
)

func TestNodeQuotaConcurrentRestartAndDedup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "quota.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	n, _, err := s.CreateNode(ctx, model.Node{Name: "limited", LimitTotalGB: 0.000001, LimitKind: "total"})
	if err != nil {
		t.Fatal(err)
	}
	var total atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g, _, err := s.ReserveNodeQuota(ctx, n.ID, fmt.Sprintf("%032d", i), 73)
			if err != nil {
				t.Error(err)
				return
			}
			total.Add(g)
		}(i)
	}
	wg.Wait()
	if total.Load() != 1000 {
		t.Fatalf("granted %d, want 1000", total.Load())
	}
	status, err := s.NodeQuota(ctx, n.ID)
	if err != nil || status.ReservedBytes != 1000 {
		t.Fatalf("status %+v %v", status, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g, _, err := s.ReserveNodeQuota(ctx, n.ID, fmt.Sprintf("%032d", 100), 1)
	if err != nil || g != 0 {
		t.Fatalf("restart grant %d %v", g, err)
	}
	_, _, err = s.ReserveNodeQuota(ctx, n.ID, fmt.Sprintf("%032d", 0), 74)
	if err == nil {
		t.Fatal("changed replay accepted")
	}
	for i := 0; i < 40; i++ {
		if _, _, err = s.ReserveNodeQuota(ctx, n.ID, fmt.Sprintf("%032d", i), 73); err != nil {
			t.Fatal(err)
		}
	}
	status, err = s.NodeQuota(ctx, n.ID)
	if err != nil || status.ReservedBytes != 1000 {
		t.Fatalf("replays charged: %+v %v", status, err)
	}
}

func TestNodeQuotaMonthlyResetAndPolicyChanges(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	n, _, err := s.CreateNode(ctx, model.Node{Name: "monthly", LimitTotalGB: 0.0000001, LimitKind: "monthly", LimitResetDay: 31})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2024, 2, 29, 23, 59, 59, 0, time.UTC)
	grant := func(ref int, at time.Time, want int64) {
		t.Helper()
		g, _, err := s.reserveNodeQuotaAt(ctx, n.ID, fmt.Sprintf("%032d", ref), 100, at)
		if err != nil || g != want {
			t.Fatalf("%v grant %d want %d: %v", at, g, want, err)
		}
	}
	grant(1, before, 100)
	grant(2, time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC), 0)
	grant(3, time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), 100)
	// Changing amount or period cannot discard historical reservations.
	n.LimitKind = "total"
	n.LimitTotalGB = 0.00000025
	if _, err = s.UpdateNode(ctx, *n); err != nil {
		t.Fatal(err)
	}
	grant(4, time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), 50)
}

func TestQuotaPeriodAndValidation(t *testing.T) {
	for _, tc := range []struct {
		at, start string
		end       string
	}{
		{"2023-02-27", "2023-01-31", "2023-02-28"},
		{"2023-02-28", "2023-02-28", "2023-03-31"},
		{"2024-02-29", "2024-02-29", "2024-03-31"},
		{"2025-01-01", "2024-12-31", "2025-01-31"},
	} {
		at, _ := time.Parse("2006-01-02", tc.at)
		start, end := quotaPeriod(model.Node{LimitKind: "monthly", LimitResetDay: 31}, at)
		if start != tc.start || time.Unix(end, 0).UTC().Format("2006-01-02") != tc.end {
			t.Fatalf("%+v got %s %d", tc, start, end)
		}
	}
	for _, n := range []model.Node{{LimitTotalGB: -1}, {LimitTotalGB: math.NaN()}, {LimitTotalGB: math.Inf(1)}, {LimitTotalGB: 1e20}, {LimitKind: "daily"}, {LimitResetDay: 32}, {LimitResetDay: -1}} {
		if ValidateNodeQuota(n) == nil {
			t.Fatalf("accepted %+v", n)
		}
	}
}
