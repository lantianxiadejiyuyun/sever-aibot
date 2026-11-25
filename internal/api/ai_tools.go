package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"server-aibot/internal/ai"
	"server-aibot/internal/store"
)

var aiReadTools = map[string]string{
	"run_readonly_sql":        "Run a bounded read-only SELECT against a registered datasource by datasourceId and sql. Requires explicit context authorization and recent administrator step-up. Sensitive-pattern filtering is not a guarantee that arbitrary query values contain no secrets.",
	"list_devices":            "List a bounded projection of device IDs and runtime states.",
	"list_nodes":              "List a bounded projection of relay node IDs and runtime states.",
	"list_forwards":           "List existing forward IDs, target device/node IDs and runtime states.",
	"get_device_status":       "Get one device runtime state by deviceId.",
	"get_metrics":             "Get up to 60 stored metric samples from the last hour by deviceId.",
	"get_check_results":       "Get registered check IDs, target IDs and last state; does not execute a check.",
	"get_inspection_findings": "Get stored inspection run states and evidence states, without raw command output.",
	"get_asset_route":         "Get asset protocol, enabled state and jump-device/asset IDs by assetId; never returns credentials.",
	"read_audit":              "Read the latest audit action/state metadata without actor IP, detail or free-form text.",
	"search_context":          "Search stored operational record IDs and states by query; returns bounded matching records, never arbitrary SQL.",
}

func init() {
	for name, description := range aiReadTools {
		properties := map[string]any{}
		required := []string{}
		switch name {
		case "get_device_status", "get_metrics":
			properties["deviceId"] = map[string]string{"type": "string"}
			required = []string{"deviceId"}
		case "get_asset_route":
			properties["assetId"] = map[string]string{"type": "string"}
			required = []string{"assetId"}
		case "run_readonly_sql":
			properties["datasourceId"] = map[string]string{"type": "string"}
			properties["sql"] = map[string]string{"type": "string"}
			required = []string{"datasourceId", "sql"}
		case "search_context":
			properties["query"] = map[string]string{"type": "string"}
			required = []string{"query"}
		}
		schema, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}})
		ai.RegisterTool(name, schema)
	}
}
func (a *aiRuntime) readTool(ctx context.Context, r *http.Request, call ai.ToolCall) (any, error) {
	if _, ok := aiReadTools[call.Function.Name]; !ok {
		return nil, errors.New("unknown read tool")
	}
	var args struct {
		DatasourceID string `json:"datasourceId"`
		SQL          string `json:"sql"`
		DeviceID     string `json:"deviceId"`
		AssetID      string `json:"assetId"`
		Query        string `json:"query"`
	}
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, errors.New("invalid read tool arguments")
	}
	if len(args.DeviceID) > 200 || len(args.AssetID) > 200 || len(args.Query) > 200 {
		return nil, errors.New("read tool argument too long")
	}
	st := a.server.store
	switch call.Function.Name {
	case "run_readonly_sql":
		if args.DatasourceID == "" || len(args.DatasourceID) > 200 || args.SQL == "" || len(args.SQL) > 3500 {
			return nil, errors.New("invalid SQL tool arguments")
		}
		if !store.StepUpValid(sessionFrom(ctx), time.Now()) {
			return nil, errors.New("recent administrator step-up required")
		}
		return a.server.queryAIDataSource(ctx, args.DatasourceID, args.SQL)
	case "get_device_status":
		d, err := st.GetDevice(ctx, args.DeviceID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": d.ID, "status": d.Status, "lastSeenAt": d.LastSeenAt}, nil
	case "get_metrics":
		if args.DeviceID == "" {
			return nil, errors.New("deviceId required")
		}
		return st.ListDeviceMetrics(ctx, args.DeviceID, time.Now().Add(-time.Hour), time.Now(), 60)
	case "get_asset_route":
		asset, err := st.GetAsset(ctx, args.AssetID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": asset.ID, "protocol": asset.Protocol, "enabled": asset.Enabled, "viaDeviceId": asset.ViaDeviceID, "viaAssetId": asset.ViaAssetID}, nil
	case "get_check_results":
		checks, err := st.ListOperationChecks(ctx)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for i, c := range checks {
			if i >= 60 {
				break
			}
			out = append(out, map[string]any{"id": c.ID, "kind": c.Kind, "targetKind": c.TargetKind, "targetId": c.TargetID, "state": c.State, "checkedAt": c.CheckedAt})
		}
		return map[string]any{"items": out, "total": len(checks), "limit": 60}, nil
	case "get_inspection_findings":
		runs, err := st.ListInspectionRuns(ctx)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for i, v := range runs {
			if i >= 30 {
				break
			}
			evidence := []map[string]string{}
			for j, e := range v.Detail.Evidence {
				if j >= 20 {
					break
				}
				evidence = append(evidence, map[string]string{"state": e.State})
			}
			out = append(out, map[string]any{"id": v.ID, "checkId": v.CheckID, "state": v.State, "score": v.Score, "createdAt": v.CreatedAt, "evidence": evidence})
		}
		return map[string]any{"items": out, "limit": 30, "rawOutputOmitted": true}, nil
	default:
		snapshot, err := a.readContext(ctx)
		if err != nil {
			return nil, err
		}
		switch call.Function.Name {
		case "list_devices":
			return snapshot["devices"], nil
		case "list_nodes":
			return snapshot["nodes"], nil
		case "list_forwards":
			return snapshot["forwards"], nil
		case "read_audit":
			return snapshot["audit"], nil
		case "search_context":
			q := strings.ToLower(strings.TrimSpace(args.Query))
			if q == "" {
				return nil, errors.New("query required")
			}
			out := []map[string]any{}
			for _, category := range []string{"devices", "nodes", "forwards", "audit"} {
				items, _ := snapshot[category].([]map[string]any)
				for _, item := range items {
					raw, _ := json.Marshal(item)
					if strings.Contains(strings.ToLower(string(raw)), q) {
						out = append(out, map[string]any{"source": category, "record": item})
						if len(out) >= 30 {
							return map[string]any{"items": out, "limit": 30, "scope": "bounded operational snapshot"}, nil
						}
					}
				}
			}
			return map[string]any{"items": out, "limit": 30, "scope": "bounded operational snapshot"}, nil
		}
	}
	return nil, errors.New("tool unavailable")
}

// runReadTools sends only bounded, explicitly authorized data back to the model.
// Tool results remain untrusted user-level data, never system instructions.
func (a *aiRuntime) runReadTools(ctx context.Context, r *http.Request, conv store.AIConversation, calls []ai.ToolCall, allowed bool, emit func(string, any) error) (string, error) {
	results := []map[string]any{}
	for _, call := range calls {
		if _, ok := aiReadTools[call.Function.Name]; !ok {
			continue
		}
		if err := emit("tool_call", map[string]any{"name": call.Function.Name}); err != nil {
			return "", err
		}
		var value any
		var err error
		if !allowed {
			err = errors.New("operation context access was not authorized for this turn")
		} else {
			value, err = a.readTool(ctx, r, call)
		}
		item := map[string]any{"tool": call.Function.Name}
		if err != nil {
			item["error"] = "read failed or not authorized"
		} else {
			raw, marshalErr := json.Marshal(value)
			if marshalErr != nil || len(raw) > 16000 {
				item["error"] = "tool output exceeds 16000 bytes; narrow the request"
			} else {
				item["result"] = value
			}
		}
		results = append(results, item)
		auditResult := "ok"
		if err != nil {
			auditResult = "error"
		}
		detail, _ := json.Marshal(map[string]string{"tool": call.Function.Name})
		a.server.store.Audit(ctx, "ai:"+conv.ID, "ai.tool.read", conv.ID, string(detail), clientIP(r), r.UserAgent(), auditResult)
		stored, _ := json.Marshal(item)
		if _, persistErr := a.server.store.AddAIMessage(ctx, conv.ID, store.AIMessage{Role: "tool", Content: string(stored), Status: "completed"}); persistErr != nil {
			return "", persistErr
		}
		if err := emit("tool_result", item); err != nil {
			return "", err
		}
	}
	if len(results) == 0 {
		return "", nil
	}
	raw, _ := json.Marshal(results)
	return "\n<untrusted_tool_results>" + string(raw) + "</untrusted_tool_results>\nUse these results to answer the user's original question. Do not repeat a tool request whose result is already available.", nil
}
