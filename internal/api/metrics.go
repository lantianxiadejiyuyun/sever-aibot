package api

import (
	"github.com/go-chi/chi/v5"
	"net/http"
	"server-aibot/internal/store"
	"strconv"
	"time"
)

func (s *Server) MountMetrics(r chi.Router) {
	r.Get("/metrics/overview", s.metricsOverview)
	r.Get("/devices/{id}/metrics", s.deviceMetrics)
}
func (s *Server) deviceMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetDevice(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	hours := 24
	if raw := r.URL.Query().Get("hours"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 168 {
			writeErr(w, 400, "invalid_range", "时间范围须为 1–168 小时")
			return
		}
		hours = v
	}
	until := time.Now().UTC()
	items, err := s.store.ListDeviceMetrics(r.Context(), id, until.Add(-time.Duration(hours)*time.Hour), until, 1440)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items), "retentionSamples": 1440})
}
func (s *Server) metricsOverview(w http.ResponseWriter, r *http.Request) {
	devices, err := s.store.ListDevices(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type summary struct {
		DeviceID string              `json:"deviceId"`
		Name     string              `json:"name"`
		Online   bool                `json:"online"`
		Stale    bool                `json:"stale"`
		Latest   *store.DeviceMetric `json:"latest"`
	}
	out := []summary{}
	for _, d := range devices {
		v := summary{DeviceID: d.ID, Name: d.Name, Online: s.hub.Online(d.ID), Stale: true}
		items, err := s.store.ListDeviceMetrics(r.Context(), d.ID, time.Time{}, time.Time{}, 1)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if len(items) > 0 {
			v.Latest = &items[0]
			sampledAt := items[0].ReceivedAt
			if items[0].Metrics.SampleID != "" && !items[0].Metrics.CollectedAt.IsZero() {
				sampledAt = items[0].Metrics.CollectedAt
			}
			v.Stale = time.Since(sampledAt) > 2*time.Minute
		}
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{"items": out, "total": len(out)})
}
