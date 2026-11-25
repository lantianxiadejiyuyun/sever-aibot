package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"server-aibot/internal/hub"
	"server-aibot/internal/model"
	"server-aibot/internal/store"
)

func TestMetricOutboxDeliveryThroughHub(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "metrics.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	device, key, err := st.CreateDevice(ctx, "delivery", "", "")
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(st, quietLogger())
	server := httptest.NewServer(controlOnly(func(w http.ResponseWriter, r *http.Request) { h.Serve(device.ID, w, r) }))
	defer server.Close()
	defer cancel()
	client := newTestClient(t, Config{ControlURL: server.URL, DeviceID: device.ID, DeviceKey: key, HeartbeatInterval: 20 * time.Millisecond}, quietLogger())
	client.metrics.add(model.Metrics{CollectedAt: time.Now().Add(-time.Hour), Status: map[string]string{"cpu": "unavailable"}})
	_, before := client.metrics.snapshot()
	oldID := before[0].SampleID
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	waitFor(t, 3*time.Second, "backfill persisted and acknowledged", func() bool {
		rows, e := st.ListDeviceMetrics(ctx, device.ID, time.Time{}, time.Time{}, 10)
		if e != nil || len(rows) != 2 {
			return false
		}
		if rows[0].Metrics.SampleID == oldID || rows[1].Metrics.SampleID != oldID {
			return false
		}
		_, pending := client.metrics.snapshot()
		return len(pending) == 0
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop")
	}
}
