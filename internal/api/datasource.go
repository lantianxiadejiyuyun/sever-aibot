package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"server-aibot/internal/datasource"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

var datasourceSlots = make(chan struct{}, 8)
var errDatasourceBusy = errors.New("database query concurrency limit reached")

func (s *Server) executeDataSourceRead(ctx context.Context, d *store.DataSource, query string, schema bool) (*datasource.Result, error) {
	if !d.Enabled {
		return nil, errors.New("datasource disabled")
	}
	if err := s.validateDataSource(ctx, d); err != nil {
		return nil, err
	}
	if !schema {
		if err := datasource.ValidateSQL(query); err != nil {
			return nil, err
		}
	}
	select {
	case datasourceSlots <- struct{}{}:
		defer func() { <-datasourceSlots }()
	default:
		return nil, errDatasourceBusy
	}
	a, err := s.store.GetAsset(ctx, d.AssetID)
	if err != nil {
		return nil, err
	}
	v, err := vault.New(s.cfg.MasterKey)
	if err != nil {
		return nil, errors.New("vault unavailable")
	}
	secret, err := s.store.RevealCredential(ctx, v, d.CredentialID)
	if err != nil {
		return nil, errors.New("database credential unavailable")
	}
	defer zeroBytes(secret.Plaintext)
	cfg := datasource.Config{Protocol: a.Protocol, Host: a.Address, Database: d.Database, Username: secret.Username, Secret: string(secret.Plaintext), TLSMode: d.TLSMode, Dial: func(ctx context.Context) (net.Conn, error) { return s.dialAsset(ctx, d.AssetID) }}
	var result *datasource.Result
	if schema {
		result, err = datasource.Schema(ctx, cfg)
	} else {
		result, err = datasource.Query(ctx, cfg, query)
	}
	if err != nil {
		return nil, errors.New("database readonly request failed; verify connection, TLS, database, permissions and SQL")
	}
	_ = s.store.TouchCredentialUsed(ctx, d.CredentialID)
	return result, nil
}

// queryAIDataSource shares the HTTP query safety boundary. The AI caller must
// enforce explicit context sharing and a fresh authenticated step-up. Redaction
// is conservative and cannot identify secrets in arbitrarily named columns.
func (s *Server) queryAIDataSource(ctx context.Context, id, query string) (*datasource.Result, error) {
	d, err := s.store.GetDataSource(ctx, id)
	if err != nil {
		return nil, err
	}
	result, err := s.executeDataSourceRead(ctx, d, query, false)
	if err != nil {
		return nil, err
	}
	sensitive := func(value string) bool {
		v := strings.ToLower(value)
		for _, word := range []string{"password", "passwd", "secret", "token", "credential", "private_key", "private key", "api_key", "authorization", "cookie", "session", "bearer ", "-----begin"} {
			if strings.Contains(v, word) {
				return true
			}
		}
		return false
	}
	querySensitive := sensitive(query)
	for _, row := range result.Rows {
		for i, value := range row {
			if querySensitive || sensitive(result.Columns[i]) {
				row[i] = "[redacted]"
				continue
			}
			if text, ok := value.(string); ok {
				if sensitive(text) {
					row[i] = "[redacted]"
				} else if len(text) > 1000 {
					row[i] = text[:1000] + "…"
				}
			}
		}
	}
	if len(result.Rows) > 50 {
		result.Rows = result.Rows[:50]
		result.Truncated = true
	}
	return result, nil
}

func (s *Server) MountDataSources(r chi.Router) {
	r.Route("/datasources", func(r chi.Router) {
		r.Get("/", s.listDataSources)
		r.With(s.requireCSRF).Post("/", s.createDataSource)
		r.Route("/{id}", func(r chi.Router) {
			r.With(s.requireCSRF).Patch("/", s.updateDataSource)
			r.With(s.requireCSRF).Delete("/", s.deleteDataSource)
			r.Get("/schema", s.dataSourceSchema)
			r.With(s.requireCSRF).Post("/query", s.queryDataSource)
		})
	})
}
func (s *Server) validateDataSource(ctx context.Context, d *store.DataSource) error {
	d.Name = strings.TrimSpace(d.Name)
	d.Database = strings.TrimSpace(d.Database)
	if d.Name == "" || len(d.Name) > 200 || d.Database == "" || len(d.Database) > 128 || strings.ContainsAny(d.Database, "\x00\r\n") {
		return errors.New("数据源名称和数据库名必填且长度不能超限")
	}
	if d.TLSMode == "" {
		d.TLSMode = "verify-full"
	}
	if d.TLSMode != "verify-full" && d.TLSMode != "disable" {
		return errors.New("TLS 模式必须为 verify-full 或 disable")
	}
	a, err := s.store.GetAsset(ctx, d.AssetID)
	if err != nil {
		return errors.New("资产不存在")
	}
	if a.Protocol != "postgres" && a.Protocol != "mysql" {
		return errors.New("请选择 PostgreSQL 或 MySQL 资产")
	}
	if !a.Enabled {
		return errors.New("资产已禁用")
	}
	if d.CredentialID == "" {
		d.CredentialID = a.CredentialID
	}
	if d.CredentialID == "" || d.CredentialID != a.CredentialID {
		return errors.New("数据源凭据必须与资产关联凭据一致")
	}
	c, err := s.store.GetCredential(ctx, d.CredentialID)
	if err != nil {
		return errors.New("凭据不存在")
	}
	if c.Kind != "db_password" || c.Username == "" {
		return errors.New("需要含用户名的数据库口令凭据")
	}
	return nil
}
func (s *Server) listDataSources(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDataSources(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, page(items))
}
func (s *Server) createDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	d := store.DataSource{Enabled: true}
	if !decodeJSON(w, r, &d) {
		return
	}
	if err := s.validateDataSource(r.Context(), &d); err != nil {
		writeErr(w, 400, "invalid_datasource", err.Error())
		return
	}
	created, err := s.store.CreateDataSource(r.Context(), d)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "datasource.create", created.ID, map[string]string{"assetId": created.AssetID})
	writeJSON(w, 201, created)
}
func (s *Server) updateDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	d, err := s.store.GetDataSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	id := d.ID
	if !decodeJSON(w, r, d) {
		return
	}
	d.ID = id
	if err = s.validateDataSource(r.Context(), d); err != nil {
		writeErr(w, 400, "invalid_datasource", err.Error())
		return
	}
	updated, err := s.store.UpdateDataSource(r.Context(), *d)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "datasource.update", id, nil)
	writeJSON(w, 200, updated)
}
func (s *Server) deleteDataSource(w http.ResponseWriter, r *http.Request) {
	if !s.accessStepUp(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteDataSource(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "datasource.delete", id, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) dataSourceSchema(w http.ResponseWriter, r *http.Request) {
	s.runDataSource(w, r, true)
}
func (s *Server) queryDataSource(w http.ResponseWriter, r *http.Request) {
	s.runDataSource(w, r, false)
}
func (s *Server) runDataSource(w http.ResponseWriter, r *http.Request, schema bool) {
	if !s.accessStepUp(w, r) {
		return
	}
	var req struct {
		SQL string `json:"sql"`
	}
	if !schema {
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := datasource.ValidateSQL(req.SQL); err != nil {
			writeErr(w, 400, "readonly_sql_required", err.Error())
			return
		}
	}
	id := chi.URLParam(r, "id")
	d, err := s.store.GetDataSource(r.Context(), id)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !d.Enabled {
		writeErr(w, 409, "datasource_disabled", "数据源已禁用")
		return
	}
	if err = s.validateDataSource(r.Context(), d); err != nil {
		writeErr(w, 409, "invalid_datasource", err.Error())
		return
	}
	result, err := s.executeDataSourceRead(r.Context(), d, req.SQL, schema)
	if errors.Is(err, errDatasourceBusy) {
		writeErr(w, 429, "query_limit", "并发查询已达上限，请稍后重试")
		return
	}
	action := "datasource.query"
	if schema {
		action = "datasource.schema"
	}
	if err != nil {
		s.auditWithResult(r, action, id, map[string]string{"assetId": d.AssetID}, "error")
		writeErr(w, 409, "query_failed", "数据库只读请求失败，请检查连通性、TLS、数据库名、只读账号权限及 SQL；超时上限 8 秒")
		return
	}
	s.audit(r, action, id, map[string]any{"rows": len(result.Rows), "truncated": result.Truncated, "durationMs": result.DurationMS})
	writeJSON(w, 200, result)
}
