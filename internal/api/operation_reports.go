package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/operations"
	"server-aibot/internal/store"
)

func (s *Server) listInspectionBatches(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListInspectionBatches(r.Context())
	if err != nil {
		s.operationError(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}
func (s *Server) getInspectionBatch(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetInspectionBatch(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.operationError(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) createInspectionBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string   `json:"name"`
		CheckIDs []string `json:"checkIds"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	batch, err := s.operationService().StartBatch(r.Context(), in.Name, in.CheckIDs)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, operations.ErrBusy) {
			s.operationError(w, err)
		} else {
			writeErr(w, 400, "invalid_batch", err.Error())
		}
		return
	}
	s.operationAudit(r, "inspection.batch.create", batch.ID)
	writeJSON(w, http.StatusAccepted, batch)
}
