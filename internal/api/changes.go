package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/access"
	"server-aibot/internal/store"
)

type changeProposalInput struct {
	AssetID    string `json:"assetId"`
	Kind       string `json:"kind"`
	Service    string `json:"service"`
	ConfigPath string `json:"configPath"`
	Content    string `json:"content"`
	Summary    string `json:"summary"`
}
type changeConfirmation struct {
	Revision  string `json:"revision"`
	Confirmed bool   `json:"confirmed"`
	OTP       string `json:"otp"`
}
type changesRuntime struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
}

var changesRuntimes sync.Map

func (s *Server) changeState() *changesRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	candidate := &changesRuntime{ctx: ctx, cancel: cancel}
	actual, loaded := changesRuntimes.LoadOrStore(s, candidate)
	if loaded {
		cancel()
	}
	return actual.(*changesRuntime)
}
func (s *Server) CloseChanges() {
	v, ok := changesRuntimes.LoadAndDelete(s)
	if !ok {
		return
	}
	rt := v.(*changesRuntime)
	rt.mu.Lock()
	rt.closed = true
	rt.cancel()
	rt.mu.Unlock()
	rt.wg.Wait()
}
func (s *Server) MountChanges(r chi.Router) {
	if err := s.store.RecoverChanges(context.Background()); err != nil {
		s.log.Error("recover changes", "err", err)
	}
	r.Route("/changes", func(r chi.Router) {
		r.Get("/", s.listChanges)
		r.With(s.requireCSRF).Post("/", s.createChange)
		r.Get("/capabilities", s.changeCapabilities)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.getChange)
			r.Get("/executions", s.changeExecutions)
			r.With(s.requireCSRF).Post("/approve", s.approveChange)
			r.With(s.requireCSRF).Post("/execute", s.executeChange)
			r.With(s.requireCSRF).Post("/archive", s.archiveChange)
		})
	})
	r.Get("/deployment-archives", s.listDeploymentArchives)
}
func (s *Server) changeCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"executionEnabled": os.Getenv("AIBOT_ENABLE_SSH_CHANGES") == "1", "services": []string{"nginx", "caddy"}, "configPaths": map[string]string{"nginx": "/etc/nginx/conf.d/aibot-managed.conf", "caddy": "/etc/caddy/Caddyfile"}, "requiredAssetTag": "managed:manual", "productionAllowed": false})
}
func (s *Server) validateChangeTarget(ctx context.Context, id string) error {
	a, err := s.store.GetAsset(ctx, id)
	if err != nil {
		return err
	}
	if !a.Enabled || a.Protocol != "ssh" || a.HostKey == "" {
		return errors.New("变更目标需要已启用且已固定指纹的 SSH 资产")
	}
	manual := false
	for _, tag := range strings.Split(strings.ToLower(a.Tags), ",") {
		tag = strings.TrimSpace(tag)
		if tag == "managed:manual" {
			manual = true
		}
		if strings.Contains(tag, "baota") || tag == "bt" || tag == "bt-managed" || strings.Contains(tag, "production") {
			return errors.New("宝塔托管或生产目标禁止 SSH 变更，请使用宝塔 API 部署流程")
		}
	}
	if !manual {
		return errors.New("仅允许显式标记 managed:manual 的非生产资产")
	}
	return nil
}
func changeRevision(plan []byte) string {
	sum := sha256.Sum256(plan)
	return hex.EncodeToString(sum[:])
}
func (s *Server) previewChange(ctx context.Context, actor string, input changeProposalInput) (store.ChangeProposal, error) {
	if len(input.Summary) > 500 {
		return store.ChangeProposal{}, errors.New("提案摘要过长")
	}
	if err := s.validateChangeTarget(ctx, input.AssetID); err != nil {
		return store.ChangeProposal{}, err
	}
	engine, err := s.accessEngine()
	if err != nil {
		return store.ChangeProposal{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	plan, err := engine.PreviewChange(ctx, access.ChangePlan{AssetID: input.AssetID, Kind: input.Kind, Service: input.Service, ConfigPath: input.ConfigPath, Content: input.Content})
	if err != nil {
		return store.ChangeProposal{}, err
	}
	body, _ := json.Marshal(plan)
	proposal := store.ChangeProposal{AssetID: input.AssetID, Kind: input.Kind, Service: input.Service, Summary: strings.TrimSpace(input.Summary), Actor: actor, Plan: body, Revision: changeRevision(body), Status: "draft", Risk: "medium", Impact: "服务重启会造成短暂连接中断；仅限手工管理的非生产目标。", Rollback: "服务重启无法恢复进程内存状态；失败后需人工检查服务日志。", Verification: "固定 systemctl is-active 检查服务活跃状态；不等同于应用业务健康检查。", Diff: "restart " + input.Service}
	if proposal.Summary == "" {
		proposal.Summary = input.Kind + " " + input.Service
	}
	if input.Kind == "config_update" {
		proposal.Risk = "high"
		proposal.Diff = "--- previous " + input.ConfigPath + "\n+++ proposed " + input.ConfigPath + "\n-" + strings.ReplaceAll(plan.BeforeContent, "\n", "\n-") + "\n+" + strings.ReplaceAll(plan.Content, "\n", "\n+")
		proposal.Impact = "替换固定配置并重启服务，配置可能包含可执行指令或影响流量/访问控制；仅限手工管理的非生产目标。"
		proposal.Rollback = "变更前同目录备份并核对原始 SHA256；配置校验、重启或活跃验证失败时恢复旧文件（原先不存在则删除新文件），再次校验及重启。远端宕机/强制杀死可能无法回滚，必须人工核验。"
		proposal.Verification = "nginx -t 或 caddy validate；systemctl restart；systemctl is-active。审批及执行前核对固定指纹、资产链和配置 SHA256。"
	}
	return proposal, nil
}
func (s *Server) createProposedChange(ctx context.Context, actor string, input changeProposalInput) (*store.ChangeProposal, error) {
	p, err := s.previewChange(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	return s.store.CreateChangeProposal(ctx, p)
}
func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListChangeProposals(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}
func (s *Server) getChange(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetChangeProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) createChange(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	var input changeProposalInput
	if !decodeJSON(w, r, &input) {
		return
	}
	p, err := s.createProposedChange(r.Context(), sessionFrom(r.Context()).Username, input)
	if err != nil {
		s.accessError(w, err)
		return
	}
	s.audit(r, "change.propose", p.ID, map[string]string{"assetId": p.AssetID, "kind": p.Kind, "revision": p.Revision})
	writeJSON(w, 201, p)
}
func (s *Server) approveChange(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	var req changeConfirmation
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.store.GetChangeProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !req.Confirmed || p.Status != "draft" || p.Revision != req.Revision {
		writeErr(w, 409, "proposal_changed", "需明确确认当前未审批提案的版本")
		return
	}
	if err = s.validateChangeTarget(r.Context(), p.AssetID); err != nil {
		s.accessError(w, err)
		return
	}
	if p.Risk == "high" && !s.RequireHighRiskFactor(w, r, req.OTP) {
		return
	}
	if err = s.store.ApproveChange(r.Context(), p.ID, p.Revision, sessionFrom(r.Context()).AdminID); err != nil {
		s.accessError(w, err)
		return
	}
	s.audit(r, "change.approve", p.ID, map[string]string{"revision": p.Revision})
	p, _ = s.store.GetChangeProposal(r.Context(), p.ID)
	writeJSON(w, 200, p)
}
func (s *Server) runClaimedChange(p *store.ChangeProposal, run *store.ChangeExecution) (*store.ChangeExecution, error) {
	rt := s.changeState()
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil, errors.New("change executor closed")
	}
	rt.wg.Add(1)
	rt.mu.Unlock()
	defer rt.wg.Done()
	ctx, cancel := context.WithTimeout(rt.ctx, 2*time.Minute)
	defer cancel()
	var plan access.ChangePlan
	err := json.Unmarshal(p.Plan, &plan)
	if err == nil && changeRevision(p.Plan) != p.Revision {
		err = errors.New("proposal snapshot mismatch")
	}
	if err == nil {
		err = s.validateChangeTarget(ctx, p.AssetID)
	}
	run.Status = "failed"
	if err == nil {
		var engine *access.Engine
		engine, err = s.accessEngine()
		if err == nil {
			var result access.ChangeResult
			result, err = engine.ExecuteChange(ctx, plan, run.ID)
			if result.Status != "" {
				run.Status = result.Status
			}
			run.Output = result.Output
			run.BackupPath = result.BackupPath
		}
	}
	if err != nil && run.Output == "" {
		run.Output = err.Error()
	}
	saveErr := s.store.FinishChange(context.Background(), *run)
	if saveErr != nil {
		return run, saveErr
	}
	return run, err
}
func (s *Server) executeChange(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	if os.Getenv("AIBOT_ENABLE_SSH_CHANGES") != "1" {
		writeErr(w, 403, "execution_disabled", "SSH 变更执行默认关闭，仅可在非生产验证环境显式开启；生产部署使用宝塔 API")
		return
	}
	var req changeConfirmation
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.store.GetChangeProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !req.Confirmed || p.Revision != req.Revision || p.Status != "approved" {
		writeErr(w, 409, "proposal_not_approved", "需确认当前已审批提案版本")
		return
	}
	if err = s.validateChangeTarget(r.Context(), p.AssetID); err != nil {
		s.accessError(w, err)
		return
	}
	// The approval is bound to the exact revision, approver and five-minute TTL.
	// High-risk OTP was consumed at approval; execution cannot alter that snapshot.
	run, err := s.store.ClaimChange(r.Context(), p.ID, p.Revision, sessionFrom(r.Context()).AdminID)
	if err != nil {
		s.accessError(w, err)
		return
	}
	s.audit(r, "change.execute", p.ID, map[string]string{"executionId": run.ID})
	result, err := s.runClaimedChange(p, run)
	if err != nil {
		s.auditWithResult(r, "change.finish", p.ID, map[string]string{"status": run.Status}, "error")
	} else {
		s.audit(r, "change.finish", p.ID, map[string]string{"status": run.Status})
	}
	if result == nil {
		s.accessError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) changeExecutions(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListChangeExecutions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}
func (s *Server) archiveChange(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 200 || strings.TrimSpace(req.Version) == "" || len(req.Version) > 100 {
		writeErr(w, 400, "invalid_archive", "档案名称和版本必填且不能超长")
		return
	}
	p, err := s.store.GetChangeProposal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if p.Status != "succeeded" {
		writeErr(w, 409, "not_successful", "仅成功执行的提案可形成部署档案")
		return
	}
	a, err := s.store.CreateDeploymentArchive(r.Context(), store.DeploymentArchive{Name: req.Name, Version: req.Version, ProposalID: p.ID, AssetID: p.AssetID, Plan: p.Plan, Digest: p.Revision, CreatedBy: sessionFrom(r.Context()).Username})
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "deployment.archive", a.ID, map[string]string{"proposalId": p.ID})
	writeJSON(w, 201, a)
}
func (s *Server) listDeploymentArchives(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDeploymentArchives(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}

func init() {
	for _, definition := range []struct{ name, kind string }{{"propose_service_restart", "service_restart"}, {"propose_config_change", "config_update"}, {"propose_deploy", "config_update"}} {
		definition := definition
		schema, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": definition.name, "description": "Create a fixed SSH change proposal for a manually managed non-production asset. Never executes before human approval; Baota production deployment is forbidden.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"assetId": map[string]string{"type": "string"}, "service": map[string]any{"type": "string", "enum": []string{"nginx", "caddy"}}, "configPath": map[string]string{"type": "string"}, "content": map[string]string{"type": "string"}, "summary": map[string]string{"type": "string"}}, "required": []string{"assetId", "service"}, "additionalProperties": false}}})
		RegisterAIAction(definition.name, schema, func(ctx context.Context, s *Server, raw json.RawMessage) (store.AIProposal, error) {
			var input changeProposalInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return store.AIProposal{}, err
			}
			input.Kind = definition.kind
			p, err := s.previewChange(ctx, "ai", input)
			if err != nil {
				return store.AIProposal{}, err
			}
			body, _ := json.Marshal(p)
			return store.AIProposal{Target: p.AssetID, Reason: p.Summary, Risk: p.Risk, Diff: p.Diff, Impact: p.Impact, Rollback: p.Rollback, Verification: p.Verification, Revision: 1, Args: body}, nil
		}, func(ctx context.Context, s *Server, aiProposal store.AIProposal) (string, string, error) {
			if aiProposal.ApprovedBy == "" || os.Getenv("AIBOT_ENABLE_SSH_CHANGES") != "1" {
				return "failed", "SSH change execution is disabled or approval is missing", errors.New("execution not authorized")
			}
			var snapshot store.ChangeProposal
			if err := json.Unmarshal(aiProposal.Args, &snapshot); err != nil {
				return "failed", "invalid proposal", err
			}
			if snapshot.AssetID != aiProposal.Target || changeRevision(snapshot.Plan) != snapshot.Revision {
				return "failed", "proposal snapshot mismatch", errors.New("proposal mismatch")
			}
			if err := s.validateChangeTarget(ctx, snapshot.AssetID); err != nil {
				return "failed", err.Error(), err
			}
			snapshot.Actor = "ai:" + aiProposal.ConversationID
			p, err := s.store.CreateChangeProposal(ctx, snapshot)
			if err != nil {
				return "failed", "cannot store change", err
			}
			if err = s.store.ApproveChange(ctx, p.ID, p.Revision, aiProposal.ApprovedBy); err != nil {
				return "failed", "cannot store approval", err
			}
			run, err := s.store.ClaimChange(ctx, p.ID, p.Revision, aiProposal.ApprovedBy)
			if err != nil {
				return "failed", "cannot claim change", err
			}
			result, err := s.runClaimedChange(p, run)
			if result == nil {
				return "failed", "change failed", err
			}
			return result.Status, fmt.Sprintf("proposal=%s execution=%s status=%s", p.ID, run.ID, result.Status), err
		})
	}
}
