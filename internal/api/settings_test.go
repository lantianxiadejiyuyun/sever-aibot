package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestExpiredRecordingReturnsGoneAndRetainsHistory(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	_, err := e.store.DB().ExecContext(ctx, `INSERT INTO sessions(id,actor,kind,started_at,ended_at) VALUES('expired','test','terminal','2020-01-01T00:00:00Z','2020-01-01T01:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.store.RecordAccessOutput(ctx, "expired", 0, []byte("expired output")); err != nil {
		t.Fatal(err)
	}
	if count, err := e.store.CleanupRecordings(ctx); err != nil || count != 1 {
		t.Fatalf("cleanup: %d %v", count, err)
	}
	r := e.do(t, "POST", "/api/v1/auth/step-up", map[string]string{"password": e.password}, true)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("step-up: %d", r.StatusCode)
	}
	r = e.do(t, "GET", "/api/v1/access/sessions/expired/recording", nil, false)
	r.Body.Close()
	if r.StatusCode != http.StatusGone {
		t.Fatalf("expired recording: %d", r.StatusCode)
	}
	history, err := e.store.GetAccessSession(ctx, "expired")
	if err != nil || history.RecordingPurgedAt == "" {
		t.Fatalf("history: %+v %v", history, err)
	}
}

func TestRecordingRetentionSettings(t *testing.T) {
	e := newTestEnv(t)
	r := e.do(t, "POST", "/api/v1/auth/step-up", map[string]string{"password": e.password}, true)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("step-up: %d", r.StatusCode)
	}
	for _, days := range []int{0, -1, 3651, 7} {
		r = e.do(t, "PUT", "/api/v1/settings", map[string]any{"siteName": "retention test", "auditRetentionDays": 30, "recordingRetentionDays": days}, true)
		r.Body.Close()
		want := 400
		if days == 7 {
			want = 200
		}
		if r.StatusCode != want {
			t.Fatalf("days %d: got %d want %d", days, r.StatusCode, want)
		}
	}
	r = e.do(t, "PUT", "/api/v1/settings", map[string]any{"siteName": "legacy client", "auditRetentionDays": 30}, true)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("legacy save: %d", r.StatusCode)
	}
	saved, err := e.store.Settings(context.Background())
	if err != nil || saved.RecordingRetentionDays != 7 {
		t.Fatalf("retention reset: %+v %v", saved, err)
	}
}

func TestCSVCellNeutralizesFormulas(t *testing.T) {
	for _, v := range []string{"=1+1", " +cmd", "\t@SUM(A1)", "-1+2", "\r\n=1"} {
		if got := csvCell(v); !strings.HasPrefix(got, "'") {
			t.Errorf("unsafe cell %q", v)
		}
	}
	if csvCell("ordinary") != "ordinary" {
		t.Fatal("ordinary text changed")
	}
}
func TestSettingsNeedStepUp(t *testing.T) {
	env := newTestEnv(t)
	r := env.do(t, http.MethodPut, "/api/v1/settings", map[string]any{"siteName": "Test console", "announcement": "maintenance", "auditRetentionDays": 30}, true)
	defer r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("settings without step-up = %d", r.StatusCode)
	}
	r2 := env.do(t, http.MethodGet, "/api/v1/auth/sessions", nil, false)
	defer r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatalf("sessions = %d", r2.StatusCode)
	}
}
