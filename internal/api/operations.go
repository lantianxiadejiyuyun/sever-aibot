package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/operations"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

// Kept here so integration only needs MountOperations and StartOperations.
var operationServices sync.Map

func (s *Server) operationService() *operations.Service {
	if v, ok := operationServices.Load(s); ok {
		return v.(*operations.Service)
	}
	v, _ := vault.New(s.cfg.MasterKey)
	service := operations.New(s.store, v, s.log)
	service.SetDialer(operations.DialFunc(func(ctx context.Context, t operations.Target) (net.Conn, error) {
		if t.Kind == "asset" {
			return s.dialAsset(ctx, t.ID)
		}
		if t.Kind == "forward" {
			return operations.DialPublicTarget(ctx, t)
		}
		return nil, operations.ErrUnavailable
	}))
	service.SetSSHInspector(func(ctx context.Context, c store.OperationCheck) (string, string, store.InspectionDetail) {
		e, err := s.accessEngine()
		if err != nil {
			return "stale", "SSH 加密访问服务不可用", store.InspectionDetail{Source: "ssh", Profile: c.Profile}
		}
		return operations.InspectSSH(ctx, e, c)
	})
	actual, _ := operationServices.LoadOrStore(s, service)
	return actual.(*operations.Service)
}

// SetOperationsDialer installs a real inventory-aware transport before startup.
func (s *Server) SetOperationsDialer(d operations.Dialer) { s.operationService().SetDialer(d) }

// StartOperations is non-blocking and idempotent for a Server instance.
func (s *Server) StartOperations(ctx context.Context) {
	service := s.operationService()
	service.Start(ctx)
	go func() {
		<-ctx.Done()
		if operationServices.CompareAndDelete(s, service) {
			service.Close()
		}
	}()
}

// StopOperations also releases lazily created services in httptest instances.
// Shut down HTTP requests before calling it to prevent new lazy services.
func (s *Server) StopOperations() {
	if value, ok := operationServices.LoadAndDelete(s); ok {
		value.(*operations.Service).Close()
	}
}

// MountOperations belongs inside the authenticated /api/v1 group.
func (s *Server) MountOperations(r chi.Router) {
	r.Get("/checks", s.listOperationChecks)
	r.With(s.requireCSRF).Post("/checks", s.saveOperationCheck)
	r.With(s.requireCSRF).Put("/checks/{id}", s.saveOperationCheck)
	r.With(s.requireCSRF).Delete("/checks/{id}", s.deleteOperationCheck)
	r.With(s.requireCSRF).Post("/checks/{id}/run", s.runOperationCheck)
	r.Get("/inspection-runs", s.listOperationRuns)
	r.Get("/inspection-batches", s.listInspectionBatches)
	r.Get("/inspection-batches/{id}", s.getInspectionBatch)
	r.With(s.requireCSRF).Post("/inspection-batches", s.createInspectionBatch)
	r.Get("/alerts", s.listOperationAlerts)
	r.With(s.requireCSRF).Post("/alerts/{id}/ack", s.ackOperationAlert)
	r.With(s.requireCSRF).Post("/alerts/{id}/silence", s.silenceOperationAlert)
	r.Get("/notifications", s.listOperationNotifications)
	r.With(s.requireCSRF).Post("/notifications/{id}/read", s.readOperationNotification)
	r.Get("/notify/channels", s.listOperationChannels)
	r.With(s.requireCSRF).Post("/notify/channels", s.saveOperationChannel)
	r.With(s.requireCSRF).Put("/notify/channels/{id}", s.saveOperationChannel)
	r.With(s.requireCSRF).Delete("/notify/channels/{id}", s.deleteOperationChannel)
	r.Get("/notify/outbox", s.listOperationOutbox)
	r.With(s.requireCSRF).Post("/notify/outbox/{id}/retry", s.retryOperationDelivery)
	r.Get("/notify/logs", s.listOperationLogs)
}
func (s *Server) operationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "记录不存在")
	case errors.Is(err, operations.ErrBusy) || errors.Is(err, store.ErrInUse):
		writeErr(w, 409, "busy", "检查正在运行，或记录无法修改，请刷新后重试")
	default:
		s.internal(w, "巡检通知操作失败", err)
	}
}
func (s *Server) operationAudit(r *http.Request, action, id string) {
	if sess := sessionFrom(r.Context()); sess != nil {
		s.store.Audit(r.Context(), sess.Username, action, id, "", clientIP(r), r.UserAgent(), "ok")
	}
}
func (s *Server) listOperationChecks(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListOperationChecks(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
func (s *Server) saveOperationCheck(w http.ResponseWriter, r *http.Request) {
	var c store.OperationCheck
	if !decodeJSON(w, r, &c) {
		return
	}
	create := r.Method == http.MethodPost
	c.ID = chi.URLParam(r, "id")
	if e := s.operationService().ValidateCheck(r.Context(), c); e != nil {
		writeErr(w, 400, "invalid_check", e.Error())
		return
	}
	v, e := s.store.SaveOperationCheck(r.Context(), c, create)
	if e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "inspection.check.save", v.ID)
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, v)
}
func (s *Server) deleteOperationCheck(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if e := s.store.DeleteOperationCheck(r.Context(), id); e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "inspection.check.delete", id)
	w.WriteHeader(204)
}
func (s *Server) runOperationCheck(w http.ResponseWriter, r *http.Request) {
	v, e := s.operationService().Run(r.Context(), chi.URLParam(r, "id"))
	if e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "inspection.run", v.ID)
	writeJSON(w, 200, v)
}
func (s *Server) listOperationRuns(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListInspectionRuns(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
func (s *Server) listOperationAlerts(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListOperationAlerts(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
func (s *Server) ackOperationAlert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if e := s.store.AcknowledgeOperationAlert(r.Context(), id); e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "inspection.alert.ack", id)
	w.WriteHeader(204)
}
func (s *Server) silenceOperationAlert(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Seconds int `json:"seconds"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Seconds < 0 || in.Seconds > 604800 {
		writeErr(w, 400, "invalid_duration", "静默时长必须在 0 到 604800 秒之间")
		return
	}
	until := int64(0)
	if in.Seconds > 0 {
		until = time.Now().Unix() + int64(in.Seconds)
	}
	id := chi.URLParam(r, "id")
	if e := s.store.SilenceOperationAlert(r.Context(), id, until); e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "inspection.alert.silence", id)
	w.WriteHeader(204)
}
func (s *Server) listOperationNotifications(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListOperationNotifications(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
func (s *Server) readOperationNotification(w http.ResponseWriter, r *http.Request) {
	if e := s.store.ReadOperationNotification(r.Context(), chi.URLParam(r, "id")); e != nil {
		s.operationError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) listOperationChannels(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListNotificationChannels(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
func (s *Server) saveOperationChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		store.NotificationChannel
		WebhookURL string `json:"webhookUrl"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	create := r.Method == http.MethodPost
	in.ID = chi.URLParam(r, "id")
	if in.Name == "" || len(in.Name) > 120 || (in.Kind != "feishu" && in.Kind != "webhook") {
		writeErr(w, 400, "invalid_channel", "名称不能为空且最多 120 字节，kind 必须是 feishu 或 webhook")
		return
	}
	if create || in.WebhookURL != "" {
		if e := operations.ValidateWebhook(in.WebhookURL); e != nil {
			writeErr(w, 400, "invalid_webhook", e.Error())
			return
		}
	}
	v, ok := s.newVault(w)
	if !ok {
		return
	}
	c, e := s.store.SaveNotificationChannel(r.Context(), v, in.NotificationChannel, in.WebhookURL, create)
	if e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "notification.channel.save", c.ID)
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, c)
}
func (s *Server) deleteOperationChannel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if e := s.store.DeleteNotificationChannel(r.Context(), id); e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "notification.channel.delete", id)
	w.WriteHeader(204)
}
func (s *Server) listOperationOutbox(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListNotificationOutbox(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
func (s *Server) retryOperationDelivery(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if e := s.store.RetryNotificationDelivery(r.Context(), id); e != nil {
		s.operationError(w, e)
		return
	}
	s.operationAudit(r, "notification.retry", id)
	w.WriteHeader(204)
}
func (s *Server) listOperationLogs(w http.ResponseWriter, r *http.Request) {
	v, e := s.store.ListNotificationLogs(r.Context())
	if e != nil {
		s.operationError(w, e)
		return
	}
	writeJSON(w, 200, page(v))
}
