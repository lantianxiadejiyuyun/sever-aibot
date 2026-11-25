package api

import (
	"context"
	"encoding/json"
	"net/http"
	"server-aibot/internal/model"
	"testing"
	"time"
)

func TestMetricsBackfillRemainsStale(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	device, _, err := env.store.CreateDevice(ctx, "backfill", "", "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.store.SaveDeviceMetricsBatch(ctx, device.ID, []model.Metrics{{SampleID: "00000000000000000000000000000001", CollectedAt: time.Now().UTC().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	response := env.do(t, http.MethodGet, "/api/v1/metrics/overview", nil, false)
	defer response.Body.Close()
	var body struct {
		Items []struct {
			Stale bool `json:"stale"`
		} `json:"items"`
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || !body.Items[0].Stale {
		t.Fatalf("old sample reported fresh: %+v", body)
	}
}
