// Package datasource executes a restricted SELECT surface in database-enforced
// read-only transactions. It never accepts a network destination from SQL input.
package datasource

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const MaxRows = 200
const MaxBytes = 2 << 20
const QueryTimeout = 8 * time.Second

type Config struct {
	Protocol, Host, Database, Username, Secret, TLSMode string
	Dial                                                func(context.Context) (net.Conn, error)
}
type Result struct {
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	Truncated  bool     `json:"truncated"`
	DurationMS int64    `json:"durationMs"`
}

// ValidateSQL deliberately supports a small SELECT dialect. Comments, dollar
// quoting, variables, backslash escapes, statements and arbitrary functions are
// excluded so privilege-bearing routines cannot be reached through SELECT.
func ValidateSQL(query string) error {
	if len(query) == 0 || len(query) > 16384 {
		return errors.New("SQL must contain 1-16384 bytes")
	}
	var tokens []string
	for i := 0; i < len(query); {
		c := query[i]
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if c == '\'' {
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\\' {
					return errors.New("SQL backslash escapes are not supported")
				}
				if query[i] == '\'' {
					i++
					if i < len(query) && query[i] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return errors.New("unterminated SQL string")
			}
			tokens = append(tokens, "<literal>")
			continue
		}
		if c == '"' || c == '`' {
			quote := c
			i++
			start := i
			for i < len(query) && query[i] != quote {
				if !identChar(query[i]) {
					return errors.New("quoted SQL identifiers must be simple names")
				}
				i++
			}
			if i == len(query) || i == start {
				return errors.New("invalid quoted SQL identifier")
			}
			tokens = append(tokens, strings.ToUpper(query[start:i]))
			i++
			continue
		}
		if c == ';' || c == '$' || c == '@' || c == '#' || c == '\\' || (i+1 < len(query) && ((c == '-' && query[i+1] == '-') || (c == '/' && query[i+1] == '*'))) {
			return errors.New("multiple statements, comments and variables are forbidden")
		}
		if identChar(c) {
			start := i
			for i < len(query) && identChar(query[i]) {
				i++
			}
			tokens = append(tokens, strings.ToUpper(query[start:i]))
			continue
		}
		if !strings.ContainsRune("(),.*+-/%=<>!|:&[]", rune(c)) {
			return errors.New("unsupported SQL token")
		}
		tokens = append(tokens, string(c))
		i++
	}
	if len(tokens) == 0 || tokens[0] != "SELECT" {
		return errors.New("only SELECT queries are supported")
	}
	denied := map[string]bool{}
	for _, s := range strings.Fields("INSERT UPDATE DELETE MERGE UPSERT REPLACE CREATE ALTER DROP TRUNCATE GRANT REVOKE COPY CALL DO EXEC EXECUTE INTO OUTFILE DUMPFILE LOCK UNLOCK FOR SHARE SET RESET BEGIN COMMIT ROLLBACK SAVEPOINT RELEASE ANALYZE VACUUM ATTACH DETACH PRAGMA LOAD HANDLER PROCEDURE WITH") {
		denied[s] = true
	}
	functions := map[string]bool{}
	for _, s := range strings.Fields("COUNT SUM AVG MIN MAX COALESCE NULLIF LOWER UPPER LENGTH CHAR_LENGTH TRIM LTRIM RTRIM SUBSTRING SUBSTR CONCAT CONCAT_WS ABS ROUND CEIL CEILING FLOOR MOD GREATEST LEAST CAST EXTRACT DATE_TRUNC DATE_PART TO_CHAR") {
		functions[s] = true
	}
	syntax := map[string]bool{"IN": true, "EXISTS": true, "AS": true, "OVER": true, "SELECT": true}
	for i, t := range tokens {
		if denied[t] {
			return fmt.Errorf("SQL token %s is forbidden", t)
		}
		if i+1 < len(tokens) && tokens[i+1] == "(" && len(t) > 0 && identChar(t[0]) {
			if syntax[t] {
				continue
			}
			if !functions[t] || (i > 0 && tokens[i-1] == ".") {
				return fmt.Errorf("SQL function %s is not allowed", t)
			}
		}
	}
	return nil
}
func identChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func openDB(c Config) (*sql.DB, error) {
	if c.Dial == nil || c.Database == "" || c.Username == "" {
		return nil, errors.New("database configuration incomplete")
	}
	if c.TLSMode != "verify-full" && c.TLSMode != "disable" {
		return nil, errors.New("unsupported TLS mode")
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) { return c.Dial(ctx) }
	var db *sql.DB
	switch c.Protocol {
	case "postgres":
		cfg, err := pgx.ParseConfig("postgres://localhost/postgres?sslmode=disable")
		if err != nil {
			return nil, err
		}
		cfg.Host = c.Host
		cfg.Database = c.Database
		cfg.User = c.Username
		cfg.Password = c.Secret
		cfg.DialFunc = dial
		cfg.LookupFunc = func(context.Context, string) ([]string, error) { return []string{c.Host}, nil }
		cfg.Fallbacks = nil
		cfg.ConnectTimeout = QueryTimeout
		cfg.RuntimeParams = map[string]string{"application_name": "server-aibot-readonly", "default_transaction_read_only": "on", "statement_timeout": "5000", "lock_timeout": "1000", "search_path": "pg_catalog,public"}
		if c.TLSMode == "verify-full" {
			cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.Host}
		}
		db = stdlib.OpenDB(*cfg)
	case "mysql":
		cfg := mysql.NewConfig()
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(c.Host, "3306")
		cfg.DBName = c.Database
		cfg.User = c.Username
		cfg.Passwd = c.Secret
		cfg.DialFunc = dial
		cfg.Timeout = QueryTimeout
		cfg.ReadTimeout = QueryTimeout
		cfg.WriteTimeout = QueryTimeout
		cfg.MultiStatements = false
		cfg.AllowAllFiles = false
		cfg.AllowCleartextPasswords = false
		cfg.AllowFallbackToPlaintext = false
		cfg.MaxAllowedPacket = 4 << 20
		if c.TLSMode == "verify-full" {
			cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.Host}
		}
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, err
		}
		db = sql.OpenDB(connector)
	default:
		return nil, errors.New("only PostgreSQL and MySQL assets are supported")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}
func Query(ctx context.Context, c Config, query string) (*Result, error) {
	if err := ValidateSQL(query); err != nil {
		return nil, err
	}
	return execute(ctx, c, query)
}
func Schema(ctx context.Context, c Config) (*Result, error) {
	query := `SELECT table_schema,table_name,column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema,table_name,ordinal_position`
	if c.Protocol == "mysql" {
		query = `SELECT table_schema,table_name,column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY table_name,ordinal_position`
	}
	return execute(ctx, c, query)
}

type contextConn struct {
	net.Conn
	stop func() bool
}

func (c *contextConn) Close() error { c.stop(); return c.Conn.Close() }
func execute(ctx context.Context, c Config, query string) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	started := time.Now()
	originalDial := c.Dial
	if originalDial == nil {
		return nil, errors.New("asset dialer required")
	}
	c.Dial = func(dialCtx context.Context) (net.Conn, error) {
		raw, err := originalDial(dialCtx)
		if err != nil {
			return nil, err
		}
		return &contextConn{Conn: raw, stop: context.AfterFunc(ctx, func() { raw.Close() })}, nil
	}
	db, err := openDB(c)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// MySQL's execution timeout is a server-side guard, in addition to driver
	// context cancellation and the hard row limit. Fail closed if unsupported.
	if c.Protocol == "mysql" {
		if _, err = conn.ExecContext(ctx, "SET SESSION MAX_EXECUTION_TIME=5000"); err != nil {
			return nil, err
		}
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT * FROM (%s) AS aibot_readonly LIMIT %d", query, MaxRows+1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(columns) > 100 {
		return nil, errors.New("query returns more than 100 columns")
	}
	result := &Result{Columns: columns, Rows: [][]any{}}
	size := 0
	for rows.Next() {
		if len(result.Rows) >= MaxRows {
			result.Truncated = true
			break
		}
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range values {
			switch v := v.(type) {
			case []byte:
				values[i] = string(v)
				size += len(v)
			case string:
				size += len(v)
			case time.Time:
				values[i] = v.Format(time.RFC3339Nano)
				size += 40
			default:
				size += 32
			}
		}
		if size > MaxBytes {
			result.Truncated = true
			break
		}
		result.Rows = append(result.Rows, values)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Rollback(); err != nil {
		return nil, err
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}
