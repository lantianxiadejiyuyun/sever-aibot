// Package store owns the SQLite database: connection setup, embedded
// migrations and all persistence queries. Callers never see SQL.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"server-aibot/internal/model"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var ErrNotFound = errors.New("not found")

const dummyHash = "$argon2id$v=19$m=65536,t=3,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// Store is safe for concurrent use; database/sql pools the connections and
// SQLite is put in WAL mode with a busy timeout.
type Store struct {
	db *sql.DB
}

// Open creates the parent directory and applies pending migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(path)) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o600)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for packages that legitimately need transactions.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%04d", &version); err != nil {
			return fmt.Errorf("migration %s: name must start with four digits", entry.Name())
		}
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?,?)`, version, now()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- admins & sessions ----------

type Admin struct {
	ID           string
	Username     string
	PasswordHash string
}

// EnsureAdmin creates the first administrator. If an administrator already
// exists it does nothing, so restarts never overwrite a rotated argon2id.
func (s *Store) EnsureAdmin(ctx context.Context, username, argon2id string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	if strings.TrimSpace(username) == "" {
		return false, errors.New("admin username is empty")
	}
	if len(argon2id) < 8 {
		return false, errors.New("initial admin argon2id must be at least 8 characters")
	}
	hash, err := HashPassword(argon2id)
	if err != nil {
		return false, err
	}
	id := newID("adm")
	ts := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO admins(id,username,password_hash,created_at,updated_at) VALUES(?,?,?,?,?)`,
		id, username, hash, ts, ts)
	return err == nil, err
}

func (s *Store) Authenticate(ctx context.Context, username, argon2id string) (*Admin, error) {
	a := &Admin{}
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash FROM admins WHERE username=?`, username).
		Scan(&a.ID, &a.Username, &a.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		// Burn comparable time to avoid a trivial username oracle.
		VerifyPassword(dummyHash, argon2id)
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(a.PasswordHash, argon2id) {
		return nil, ErrNotFound
	}
	return a, nil
}

// Session is an authenticated browser session.
type Session struct {
	ID          string
	AdminID     string
	Username    string
	CSRF        string
	ExpiresAt   time.Time
	StepUpUntil *time.Time
}

// CreateSession returns the raw bearer token and its CSRF secret. Only the
// token hash is stored.
func (s *Store) CreateSession(ctx context.Context, adminID, ip, userAgent string, ttl time.Duration) (*Session, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return nil, "", err
	}
	id := tokenHash(token)
	expires := time.Now().UTC().Add(ttl)
	ts := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO admin_sessions(id,admin_id,csrf,ip,user_agent,created_at,expires_at,last_seen_at)
		VALUES(?,?,?,?,?,?,?,?)`, id, adminID, csrf, ip, userAgent, ts, expires.Format(time.RFC3339), ts)
	if err != nil {
		return nil, "", err
	}
	return &Session{ID: id, AdminID: adminID, CSRF: csrf, ExpiresAt: expires}, token, nil
}

func (s *Store) SessionByToken(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	sess := &Session{}
	var expires, stepUp string
	err := s.db.QueryRowContext(ctx, `SELECT s.id,s.admin_id,s.csrf,s.expires_at,COALESCE(s.step_up_until,''),a.username
		FROM admin_sessions s JOIN admins a ON a.id=s.admin_id WHERE s.id=?`, tokenHash(token)).
		Scan(&sess.ID, &sess.AdminID, &sess.CSRF, &expires, &stepUp, &sess.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	if stepUp != "" {
		if t, err := time.Parse(time.RFC3339, stepUp); err == nil {
			sess.StepUpUntil = &t
		}
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id=?`, sess.ID)
		return nil, ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE admin_sessions SET last_seen_at=? WHERE id=?`, now(), sess.ID)
	return sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id=?`, tokenHash(token))
	return err
}

func (s *Store) CleanupSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at < ?`, now())
	return err
}

// ---------- audit ----------

func (s *Store) Audit(ctx context.Context, actor, action, target, detail, ip, ua, result string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_logs(actor,action,target,detail,ip,user_agent,result,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, actor, action, target, detail, ip, ua, result, now())
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]model.AuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,actor,action,target,detail,ip,user_agent,result,created_at
		FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditLog{}
	for rows.Next() {
		var a model.AuditLog
		var created string
		if err := rows.Scan(&a.ID, &a.Actor, &a.Action, &a.Target, &a.Detail, &a.IP, &a.UserAgent, &a.Result, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------- devices ----------

func (s *Store) ListDevices(ctx context.Context) ([]model.Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,remark,tags,key_prefix,status,version,os,arch,last_seen_at,created_at,updated_at
		FROM devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(ctx context.Context, id string) (*model.Device, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,remark,tags,key_prefix,status,version,os,arch,last_seen_at,created_at,updated_at
		FROM devices WHERE id=?`, id)
	return scanDevice(row)
}

type scanner interface{ Scan(...any) error }

func scanDevice(row scanner) (*model.Device, error) {
	d := &model.Device{}
	var lastSeen sql.NullString
	var created, updated string
	err := row.Scan(&d.ID, &d.Name, &d.Remark, &d.Tags, &d.KeyPrefix, &d.Status, &d.Version, &d.OS, &d.Arch,
		&lastSeen, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.LastSeenAt = parseNullTime(lastSeen)
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return d, nil
}

// CreateDevice issues a key once and stores only its hash.
func (s *Store) CreateDevice(ctx context.Context, name, remark, tags string) (*model.Device, string, error) {
	key, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	prefix := key
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	d := &model.Device{
		ID: newID("dev"), Name: name, Remark: remark, Tags: tags,
		Status: "offline", KeyPrefix: prefix,
	}
	ts := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO devices(id,name,remark,tags,key_hash,key_prefix,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, d.Name, d.Remark, d.Tags, tokenHash(key), prefix, d.Status, ts, ts)
	if err != nil {
		return nil, "", err
	}
	d.CreatedAt, d.UpdatedAt = parseTime(ts), parseTime(ts)
	return d, key, nil
}

type DevicePatch struct {
	Name   *string
	Remark *string
	Tags   *string
}

func (s *Store) UpdateDevice(ctx context.Context, id string, p DevicePatch) (*model.Device, error) {
	if p.Name != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET name=?,updated_at=? WHERE id=?`, *p.Name, now(), id); err != nil {
			return nil, err
		}
	}
	if p.Remark != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET remark=?,updated_at=? WHERE id=?`, *p.Remark, now(), id); err != nil {
			return nil, err
		}
	}
	if p.Tags != nil {
		if _, err := s.db.ExecContext(ctx, `UPDATE devices SET tags=?,updated_at=? WHERE id=?`, *p.Tags, now(), id); err != nil {
			return nil, err
		}
	}
	return s.GetDevice(ctx, id)
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateDeviceKey returns the new key; the old one stops working immediately.
func (s *Store) RotateDeviceKey(ctx context.Context, id string) (string, error) {
	key, err := randomToken(32)
	if err != nil {
		return "", err
	}
	prefix := key[:8]
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET key_hash=?,key_prefix=?,updated_at=? WHERE id=?`,
		tokenHash(key), prefix, now(), id)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// DeviceKeyHash is used by the agent WebSocket handshake.
func (s *Store) DeviceKeyHash(ctx context.Context, id string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM devices WHERE id=?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

func (s *Store) SetDeviceOnline(ctx context.Context, id, version, osName, arch string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET status='online',version=?,os=?,arch=?,last_seen_at=?,updated_at=? WHERE id=?`,
		version, osName, arch, ts, ts, id)
	return err
}

func (s *Store) SetDeviceOffline(ctx context.Context, id string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET status='offline',last_seen_at=?,updated_at=? WHERE id=?`, ts, ts, id)
	return err
}

func (s *Store) TouchDevice(ctx context.Context, id string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET status='online',last_seen_at=?,updated_at=? WHERE id=?`, ts, ts, id)
	return err
}

// MarkAllDevicesOffline is called at startup: no WebSocket can survive a
// control-plane restart, so previous "online" rows are stale.
func (s *Store) MarkAllDevicesOffline(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET status='offline' WHERE status<>'offline'`)
	return err
}

// ---------- nodes ----------

const nodeColumns = `id,name,remark,tags,addr,protocol,http_port,replace_address,forward_ports,domain,url_template,
	deny_domain_prefix,allow_wildcard,feature_web,feature_forward,feature_tunnel,feature_p2p,feature_proxy,
	max_pool_count,p2p_disable_forward,rules,limit_total_gb,limit_kind,limit_reset_day,limiter_mbps,concurrency,connections,
	status,last_seen_at,created_at,updated_at,key_prefix`

func (s *Store) ListNodes(ctx context.Context) ([]model.Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func (s *Store) GetNode(ctx context.Context, id string) (*model.Node, error) {
	return scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id=?`, id))
}

func scanNode(row scanner) (*model.Node, error) {
	n := &model.Node{}
	var lastSeen sql.NullString
	var created, updated string
	var web, forward, tunnel, p2p, proxy, wildcard, p2pNoForward int
	err := row.Scan(&n.ID, &n.Name, &n.Remark, &n.Tags, &n.Addr, &n.Protocol, &n.HTTPPort, &n.ReplaceAddress,
		&n.ForwardPorts, &n.Domain, &n.URLTemplate, &n.DenyDomainPrefix, &wildcard,
		&web, &forward, &tunnel, &p2p, &proxy, &n.MaxPoolCount, &p2pNoForward, &n.Rules,
		&n.LimitTotalGB, &n.LimitKind, &n.LimitResetDay, &n.LimiterMbps, &n.Concurrency, &n.Connections,
		&n.Status, &lastSeen, &created, &updated, &n.KeyPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n.AllowWildcard = wildcard != 0
	n.Features = model.NodeFeatures{Web: web != 0, Forward: forward != 0, Tunnel: tunnel != 0, P2P: p2p != 0, Proxy: proxy != 0}
	n.P2PDisableForward = p2pNoForward != 0
	n.LastSeenAt = parseNullTime(lastSeen)
	n.CreatedAt = parseTime(created)
	n.UpdatedAt = parseTime(updated)
	return n, nil
}

// CreateNode issues a node key once and stores only its hash. A node cannot
// connect to /node/ws until it has a key, so every node created through the
// API gets one.
func (s *Store) CreateNode(ctx context.Context, n model.Node) (*model.Node, string, error) {
	n.ID = newID("nod")
	if n.Protocol == "" {
		n.Protocol = "tcp"
	}
	if n.Status == "" {
		n.Status = "offline"
	}
	key, err := randomToken(32)
	if err != nil {
		return nil, "", err
	}
	prefix := key
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	n.KeyPrefix = prefix
	ts := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO nodes(id,name,remark,tags,addr,protocol,http_port,replace_address,forward_ports,
		domain,url_template,deny_domain_prefix,allow_wildcard,feature_web,feature_forward,feature_tunnel,feature_p2p,feature_proxy,
		max_pool_count,p2p_disable_forward,rules,limit_total_gb,limit_kind,limit_reset_day,limiter_mbps,concurrency,connections,
		status,last_seen_at,created_at,updated_at,key_hash,key_prefix)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Name, n.Remark, n.Tags, n.Addr, n.Protocol, n.HTTPPort, n.ReplaceAddress, n.ForwardPorts, n.Domain, n.URLTemplate,
		n.DenyDomainPrefix, b2i(n.AllowWildcard), b2i(n.Features.Web), b2i(n.Features.Forward), b2i(n.Features.Tunnel),
		b2i(n.Features.P2P), b2i(n.Features.Proxy), n.MaxPoolCount, b2i(n.P2PDisableForward), n.Rules,
		n.LimitTotalGB, n.LimitKind, n.LimitResetDay, n.LimiterMbps, n.Concurrency, n.Connections, n.Status, nil, ts, ts,
		tokenHash(key), prefix)
	if err != nil {
		return nil, "", err
	}
	created, err := s.GetNode(ctx, n.ID)
	if err != nil {
		return nil, "", err
	}
	return created, key, nil
}

// RotateNodeKey returns the new node key; the old one stops working at once.
func (s *Store) RotateNodeKey(ctx context.Context, id string) (string, error) {
	key, err := randomToken(32)
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE nodes SET key_hash=?,key_prefix=?,updated_at=? WHERE id=?`,
		tokenHash(key), key[:8], now(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// NodeKeyHash is used by the node WebSocket handshake.
func (s *Store) NodeKeyHash(ctx context.Context, id string) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM nodes WHERE id=?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

func (s *Store) SetNodeOnline(ctx context.Context, id string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET status='online',last_seen_at=?,updated_at=? WHERE id=?`, ts, ts, id)
	return err
}

func (s *Store) SetNodeOffline(ctx context.Context, id string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET status='offline',last_seen_at=?,updated_at=? WHERE id=?`, ts, ts, id)
	return err
}

func (s *Store) TouchNode(ctx context.Context, id string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET status='online',last_seen_at=?,updated_at=? WHERE id=?`, ts, ts, id)
	return err
}

// MarkAllNodesOffline is called at startup: control-plane restarts drop every
// node WebSocket, so stored "online" values are stale.
func (s *Store) MarkAllNodesOffline(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE nodes SET status='offline' WHERE status<>'offline'`)
	return err
}

func (s *Store) UpdateNode(ctx context.Context, n model.Node) (*model.Node, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE nodes SET name=?,remark=?,tags=?,addr=?,protocol=?,http_port=?,replace_address=?,
		forward_ports=?,domain=?,url_template=?,deny_domain_prefix=?,allow_wildcard=?,feature_web=?,feature_forward=?,
		feature_tunnel=?,feature_p2p=?,feature_proxy=?,max_pool_count=?,p2p_disable_forward=?,rules=?,limit_total_gb=?,
		limit_kind=?,limit_reset_day=?,limiter_mbps=?,concurrency=?,connections=?,updated_at=? WHERE id=?`,
		n.Name, n.Remark, n.Tags, n.Addr, n.Protocol, n.HTTPPort, n.ReplaceAddress, n.ForwardPorts, n.Domain, n.URLTemplate,
		n.DenyDomainPrefix, b2i(n.AllowWildcard), b2i(n.Features.Web), b2i(n.Features.Forward), b2i(n.Features.Tunnel),
		b2i(n.Features.P2P), b2i(n.Features.Proxy), n.MaxPoolCount, b2i(n.P2PDisableForward), n.Rules, n.LimitTotalGB,
		n.LimitKind, n.LimitResetDay, n.LimiterMbps, n.Concurrency, n.Connections, now(), n.ID)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, ErrNotFound
	}
	return s.GetNode(ctx, n.ID)
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------- forwards ----------

const forwardColumns = `id,name,kind,COALESCE(device_id,''),COALESCE(node_id,''),client_id,target_ip,target_port,remote_port,
	domain,state,revision,last_error,tags,options,traffic_bytes,created_at,updated_at`

func (s *Store) ListForwards(ctx context.Context, deviceID, nodeID, kind string) ([]model.Forward, error) {
	query := `SELECT ` + forwardColumns + ` FROM forwards WHERE 1=1`
	args := []any{}
	if deviceID != "" {
		query += ` AND device_id=?`
		args = append(args, deviceID)
	}
	if nodeID != "" {
		query += ` AND node_id=?`
		args = append(args, nodeID)
	}
	if kind != "" {
		query += ` AND kind=?`
		args = append(args, kind)
	}
	query += ` ORDER BY name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Forward{}
	for rows.Next() {
		f, err := scanForward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

func (s *Store) GetForward(ctx context.Context, id string) (*model.Forward, error) {
	return scanForward(s.db.QueryRowContext(ctx, `SELECT `+forwardColumns+` FROM forwards WHERE id=?`, id))
}

func scanForward(row scanner) (*model.Forward, error) {
	f := &model.Forward{}
	var created, updated string
	err := row.Scan(&f.ID, &f.Name, &f.Kind, &f.DeviceID, &f.NodeID, &f.ClientID, &f.TargetIP, &f.TargetPort,
		&f.RemotePort, &f.Domain, &f.State, &f.Revision, &f.LastError, &f.Tags, &f.Options, &f.TrafficBytes, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.CreatedAt = parseTime(created)
	f.UpdatedAt = parseTime(updated)
	return f, nil
}

func (s *Store) CreateForward(ctx context.Context, f model.Forward) (*model.Forward, error) {
	f.ID = newID("fwd")
	if f.Kind == "" {
		f.Kind = "port"
	}
	if f.State == "" {
		f.State = "saved"
	}
	if f.Options == "" {
		f.Options = "{}"
	}
	ts := now()
	var deviceID, nodeID any
	if f.DeviceID != "" {
		deviceID = f.DeviceID
	}
	if f.NodeID != "" {
		nodeID = f.NodeID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO forwards(id,name,kind,device_id,node_id,client_id,target_ip,target_port,
		remote_port,domain,state,revision,last_error,tags,options,traffic_bytes,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, f.Name, f.Kind, deviceID, nodeID, f.ClientID, f.TargetIP, f.TargetPort, f.RemotePort, f.Domain,
		f.State, 0, "", f.Tags, f.Options, 0, ts, ts)
	if err != nil {
		return nil, translateConstraint(err)
	}
	return s.GetForward(ctx, f.ID)
}

func (s *Store) UpdateForward(ctx context.Context, f model.Forward) (*model.Forward, error) {
	var deviceID, nodeID any
	if f.DeviceID != "" {
		deviceID = f.DeviceID
	}
	if f.NodeID != "" {
		nodeID = f.NodeID
	}
	res, err := s.db.ExecContext(ctx, `UPDATE forwards SET name=?,kind=?,device_id=?,node_id=?,client_id=?,target_ip=?,
		target_port=?,remote_port=?,domain=?,state=?,last_error=?,tags=?,options=?,updated_at=?,revision=revision+1 WHERE id=?`,
		f.Name, f.Kind, deviceID, nodeID, f.ClientID, f.TargetIP, f.TargetPort, f.RemotePort, f.Domain,
		f.State, f.LastError, f.Tags, f.Options, now(), f.ID)
	if err != nil {
		return nil, translateConstraint(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.GetForward(ctx, f.ID)
}

func (s *Store) DeleteForward(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM forwards WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetForwardState records the outcome of a config push or node bind. Empty
// lastError clears the previous error.
func (s *Store) SetForwardState(ctx context.Context, id, state, lastError string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE forwards SET state=?,last_error=?,updated_at=? WHERE id=?`,
		state, lastError, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddForwardTraffic accumulates bytes reported by a node.
func (s *Store) AddForwardTraffic(ctx context.Context, id string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE forwards SET traffic_bytes=traffic_bytes+? WHERE id=?`, bytes, id)
	return err
}

// ---------- overview ----------

type Counts struct {
	Devices        int `json:"devices"`
	DevicesOnline  int `json:"devicesOnline"`
	Nodes          int `json:"nodes"`
	NodesOnline    int `json:"nodesOnline"`
	Forwards       int `json:"forwards"`
	ForwardsActive int `json:"forwardsActive"`
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM devices),
		(SELECT COUNT(*) FROM devices WHERE status='online'),
		(SELECT COUNT(*) FROM nodes),
		(SELECT COUNT(*) FROM nodes WHERE status='online'),
		(SELECT COUNT(*) FROM forwards),
		(SELECT COUNT(*) FROM forwards WHERE state IN ('applied','observed'))`).
		Scan(&c.Devices, &c.DevicesOnline, &c.Nodes, &c.NodesOnline, &c.Forwards, &c.ForwardsActive)
	return c, err
}

// ---------- helpers ----------

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

func parseNullTime(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	return &t
}

func newID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenDigest is used by tests and the agent handshake.
func TokenDigest(token string) string { return tokenHash(token) }

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func translateConstraint(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique") || strings.Contains(msg, "constraint") {
		return fmt.Errorf("conflict: %w", err)
	}
	return err
}

// HashPassword encodes an argon2id hash with the parameters from the plan:
// 64 MiB, 3 iterations, 4 lanes.
func HashPassword(argon2id string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const (
		memory  = 64 * 1024
		time    = 3
		threads = 4
		keyLen  = 32
	)
	key := argon2.IDKey([]byte(argon2id), salt, time, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, time, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks an encoded argon2id hash in constant time.
func VerifyPassword(encoded, argon2id string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(argon2id), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
