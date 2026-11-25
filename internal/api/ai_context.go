package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"server-aibot/internal/store"
)

func validAITask(task string) bool {
	switch task {
	case "chat", "inspection", "log_summary", "change":
		return true
	}
	return false
}
func (a *aiRuntime) taskRoutes(w http.ResponseWriter, r *http.Request) {
	items, err := a.server.store.AITaskRoutes(r.Context(), sessionFrom(r.Context()).AdminID)
	if err != nil {
		a.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (a *aiRuntime) saveTaskRoute(w http.ResponseWriter, r *http.Request) {
	if !a.stepUp(w, r) {
		return
	}
	var v store.AITaskRoute
	if !decodeJSON(w, r, &v) {
		return
	}
	if !validAITask(v.Task) {
		writeErr(w, 400, "invalid_task", "不支持的任务类型")
		return
	}
	if v.ModelID != "" && !a.availableModel(w, r, v.ModelID) {
		return
	}
	if err := a.server.store.SaveAITaskRoute(r.Context(), sessionFrom(r.Context()).AdminID, v); err != nil {
		a.storeErr(w, err)
		return
	}
	a.server.audit(r, "ai.route.update", v.Task, map[string]any{"modelId": v.ModelID})
	a.taskRoutes(w, r)
}
func (a *aiRuntime) availableModel(w http.ResponseWriter, r *http.Request, id string) bool {
	var m store.AIModel
	var p store.AIProvider
	if err := a.server.store.GetAIConfig(r.Context(), "models", id, &m); err != nil {
		a.storeErr(w, err)
		return false
	}
	if err := a.server.store.GetAIConfig(r.Context(), "providers", m.ProviderID, &p); err != nil {
		a.storeErr(w, err)
		return false
	}
	if !m.Enabled || !p.Enabled {
		writeErr(w, 400, "model_disabled", "模型或服务商已停用")
		return false
	}
	return true
}
func (a *aiRuntime) facts(w http.ResponseWriter, r *http.Request) {
	conv, ok := a.conversation(w, r)
	if !ok {
		return
	}
	v, err := a.server.store.AIFacts(r.Context(), sessionFrom(r.Context()).AdminID, conv.ID)
	if err != nil {
		a.storeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (a *aiRuntime) saveFacts(w http.ResponseWriter, r *http.Request) {
	conv, ok := a.conversation(w, r)
	if !ok {
		return
	}
	var v store.AIFacts
	if !decodeJSON(w, r, &v) {
		return
	}
	v.Content = strings.TrimSpace(v.Content)
	if !utf8.ValidString(v.Content) || len(v.Content) > 8000 || v.Revision < 0 {
		writeErr(w, 400, "invalid_facts", "固定事实最多 8000 字节，版本须有效")
		return
	}
	if err := a.server.store.SaveAIFacts(r.Context(), sessionFrom(r.Context()).AdminID, conv.ID, v); err != nil {
		a.storeErr(w, err)
		return
	}
	a.server.audit(r, "ai.facts.update", conv.ID, map[string]any{"bytes": len(v.Content)})
	a.facts(w, r)
}
