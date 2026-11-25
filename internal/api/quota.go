package api

import (
	"net/http"
	"server-aibot/internal/model"
	"server-aibot/internal/store"

	"github.com/go-chi/chi/v5"
)

func validateNodeQuota(n model.Node) error { return store.ValidateNodeQuota(n) }

func (s *Server) nodeQuota(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.NodeQuota(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
