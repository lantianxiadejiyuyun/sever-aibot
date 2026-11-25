package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"server-aibot/internal/store"
)

func init() {
	for _, d := range []struct {
		name, target, description string
		preview                   func(context.Context, *Server, json.RawMessage) (store.AIProposal, error)
		execute                   func(context.Context, *Server, store.AIProposal) (string, string, error)
	}{
		{"propose_forward_stop", "forwardId", "Propose stopping an existing forward. Requires explicit administrator approval and fresh TOTP; never stops during preview.", previewAIForwardStop, executeAIForwardStop},
		{"propose_inspection_run", "checkId", "Propose executing one registered inspection check with its existing fixed profile and target. Requires explicit administrator approval; accepts no commands or configuration overrides.", previewAIInspection, executeAIInspection},
	} {
		schema, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": d.name, "description": d.description, "parameters": map[string]any{"type": "object", "properties": map[string]any{d.target: map[string]string{"type": "string"}, "reason": map[string]string{"type": "string"}}, "required": []string{d.target, "reason"}, "additionalProperties": false}}})
		RegisterAIAction(d.name, schema, d.preview, d.execute)
	}
}

func decodeAIAction(raw json.RawMessage, target string) (string, string, error) {
	var args map[string]string
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&args); err != nil {
		return "", "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", "", errors.New("trailing action arguments")
	}
	id, reason := args[target], strings.TrimSpace(args["reason"])
	if len(args) != 2 || strings.TrimSpace(id) == "" || len(id) > 200 || reason == "" || len(reason) > 1000 {
		return "", "", errors.New("invalid action arguments")
	}
	return id, reason, nil
}

func previewAIForwardStop(ctx context.Context, s *Server, raw json.RawMessage) (store.AIProposal, error) {
	id, reason, err := decodeAIAction(raw, "forwardId")
	if err != nil {
		return store.AIProposal{}, err
	}
	f, err := s.store.GetForward(ctx, id)
	if err != nil {
		return store.AIProposal{}, err
	}
	if f.State == "stopped" || f.State == "stopping" {
		return store.AIProposal{}, errors.New("forward already stopping or stopped")
	}
	return store.AIProposal{Target: f.ID, Revision: f.Revision, Reason: reason, Risk: "high", Diff: fmt.Sprintf("停止转发 %s；期望 revision=%d", f.ID, f.Revision), Impact: "停止入口并中断现有连接；节点离线时撤销可能延迟", Rollback: "管理员核对配置后在转发页重新启动", Verification: "确认匹配新 revision 的节点撤销回执及 stopped 状态；下发不等于已关闭连接"}, nil
}

func executeAIForwardStop(ctx context.Context, s *Server, p store.AIProposal) (string, string, error) {
	if p.ApprovedBy == "" || p.Risk != "high" || p.Revision < 0 {
		return "failed", "审批无效", errors.New("invalid approval")
	}
	f, err := s.store.GetForward(ctx, p.Target)
	if err != nil || f.Revision != p.Revision || f.State == "stopping" || f.State == "stopped" {
		return "failed", "转发快照已失效，请重新提案", errors.New("stale forward")
	}
	f, err = s.store.CommandForwardChecked(ctx, p.Target, false, p.Revision)
	if err != nil {
		return "failed", "转发已变更，停止未执行", err
	}
	sent := s.pushNodeConfig(ctx, f.NodeID)
	if !s.nodes.Online(f.NodeID) {
		s.pushAgentConfig(ctx, f.DeviceID)
		s.pushAgentConfig(ctx, f.ClientID)
	}
	return "dispatched", fmt.Sprintf("forward=%s revision=%d state=%s nodeSent=%t；已保存停止请求，需核对节点撤销回执。", f.ID, f.Revision, f.State, sent), nil
}

type aiInspectionSnapshot struct {
	Check        store.OperationCheck `json:"check"`
	TargetDigest string               `json:"targetDigest"`
}

// Snapshot routing/configuration, not volatile runtime state or credential values.
func aiInspectionSnapshotFor(ctx context.Context, s *Server, c store.OperationCheck) (aiInspectionSnapshot, error) {
	snapshot := aiInspectionSnapshot{Check: c}
	snapshot.Check.State, snapshot.Check.CheckedAt = "", 0
	if err := s.operationService().ValidateCheck(ctx, c); err != nil {
		return snapshot, err
	}
	var targets []any
	switch c.TargetKind {
	case "asset":
		seen := map[string]bool{}
		for id := c.TargetID; id != ""; {
			if seen[id] || len(seen) >= 8 {
				return snapshot, errors.New("invalid asset route")
			}
			seen[id] = true
			asset, err := s.store.GetAsset(ctx, id)
			if err != nil {
				return snapshot, err
			}
			if !asset.Enabled {
				return snapshot, errors.New("disabled asset route")
			}
			targets = append(targets, asset)
			id = asset.ViaAssetID
		}
	case "forward":
		f, err := s.store.GetForward(ctx, c.TargetID)
		if err != nil {
			return snapshot, err
		}
		target, err := s.operationService().Target(ctx, c)
		if err != nil {
			return snapshot, err
		}
		targets = append(targets, f.Revision, target)
	case "device":
		targets = append(targets, c.TargetID)
	default:
		return snapshot, errors.New("invalid inspection target")
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return snapshot, err
	}
	snapshot.TargetDigest = fmt.Sprintf("%x", sha256.Sum256(raw))
	return snapshot, nil
}

func previewAIInspection(ctx context.Context, s *Server, raw json.RawMessage) (store.AIProposal, error) {
	id, reason, err := decodeAIAction(raw, "checkId")
	if err != nil {
		return store.AIProposal{}, err
	}
	c, err := s.store.GetOperationCheck(ctx, id)
	if err != nil {
		return store.AIProposal{}, err
	}
	snapshot, err := aiInspectionSnapshotFor(ctx, s, c)
	if err != nil {
		return store.AIProposal{}, err
	}
	args, err := json.Marshal(snapshot)
	if err != nil {
		return store.AIProposal{}, err
	}
	return store.AIProposal{Target: id, Reason: reason, Risk: "medium", Revision: 1, Args: args, Diff: fmt.Sprintf("执行已登记检查 %s：kind=%s target=%s/%s profile=%s", id, c.Kind, c.TargetKind, c.TargetID, c.Profile), Impact: "执行一次固定检查并保存报告；状态变化可能产生告警和通知", Rollback: "检查不修改目标配置；已生成报告和通知保留", Verification: "查看 inspection run 的 state 与证据；执行完成不代表目标健康"}, nil
}

func executeAIInspection(ctx context.Context, s *Server, p store.AIProposal) (string, string, error) {
	var snapshot aiInspectionSnapshot
	if p.ApprovedBy == "" || p.Revision != 1 || json.Unmarshal(p.Args, &snapshot) != nil || snapshot.Check.ID != p.Target {
		return "failed", "巡检审批快照无效", errors.New("invalid inspection approval")
	}
	run, err := s.operationService().RunApproved(ctx, p.Target, func(ctx context.Context, c store.OperationCheck) error {
		fresh, err := aiInspectionSnapshotFor(ctx, s, c)
		if err != nil {
			return err
		}
		if fresh != snapshot {
			return errors.New("stale inspection snapshot")
		}
		return nil
	})
	if err != nil {
		return "failed", "检查或目标快照已变更、忙碌或不可用；未完成巡检，请核对后重新提案", err
	}
	return "succeeded", fmt.Sprintf("inspectionRun=%s check=%s state=%s；报告已保存，健康结论以报告状态为准。", run.ID, run.CheckID, run.State), nil
}
