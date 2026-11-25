package datasource

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

func TestReadOnlySQLValidation(t *testing.T) {
	for _, q := range []string{"SELECT 1", "SELECT count(*) FROM public.items", "SELECT name, sum(amount) FROM items GROUP BY name", "SELECT '; DROP' AS value", `SELECT "name" FROM "items" WHERE id IN (1,2)`} {
		if err := ValidateSQL(q); err != nil {
			t.Errorf("rejected %q: %v", q, err)
		}
	}
	for _, q := range []string{"", "DELETE FROM items", "SELECT 1; DELETE FROM items", "SELECT 1 -- comment", "SELECT /*!50000 SLEEP(1) */ 1", "SELECT pg_sleep(1)", "SELECT set_config('transaction_read_only','off',false)", "SELECT evil.count(*) FROM items", "SELECT * FROM items FOR UPDATE", "SELECT * INTO OUTFILE '/tmp/x' FROM items", "WITH a AS (DELETE FROM items RETURNING *) SELECT * FROM a", "SELECT @variable:=1", "SELECT $$text$$", "SELECT 'back\\slash'", "SELECT dblink('remote','DELETE FROM items')", `SELECT "evil"."count"(*) FROM items`} {
		if err := ValidateSQL(q); err == nil {
			t.Errorf("accepted %q", q)
		}
	}
}

type queryLog struct {
	sync.Mutex
	queries []string
}

func (l *queryLog) add(s string) { l.Lock(); l.queries = append(l.queries, s); l.Unlock() }
func (l *queryLog) contains(s string) bool {
	l.Lock()
	defer l.Unlock()
	for _, q := range l.queries {
		if strings.Contains(strings.ToUpper(q), strings.ToUpper(s)) {
			return true
		}
	}
	return false
}
func pgFixture(t *testing.T) (Config, *queryLog) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	log := &queryLog{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				b := pgproto3.NewBackend(c, c)
				startup, err := b.ReceiveStartupMessage()
				if err != nil {
					return
				}
				if s, ok := startup.(*pgproto3.StartupMessage); !ok || s.Parameters["default_transaction_read_only"] != "on" {
					return
				}
				b.Send(&pgproto3.AuthenticationOk{})
				b.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "16.0"})
				b.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if b.Flush() != nil {
					return
				}
				for {
					m, err := b.Receive()
					if err != nil {
						return
					}
					switch q := m.(type) {
					case *pgproto3.Query:
						log.add(q.String)
						upper := strings.ToUpper(q.String)
						if strings.HasPrefix(upper, "SELECT") {
							b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}})
							for i := 0; i < MaxRows+1; i++ {
								b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("fixture-row")}})
							}
							b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 201")})
						} else {
							b.Send(&pgproto3.CommandComplete{CommandTag: []byte("OK")})
						}
						b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
						if b.Flush() != nil {
							return
						}
					case *pgproto3.Parse:
						log.add(q.Query)
						b.Send(&pgproto3.ParseComplete{})
					case *pgproto3.Describe:
						if q.ObjectType == 'S' {
							b.Send(&pgproto3.ParameterDescription{})
						}
						b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}})
					case *pgproto3.Bind:
						b.Send(&pgproto3.BindComplete{})
					case *pgproto3.Execute:
						for i := 0; i < MaxRows+1; i++ {
							b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("fixture-row")}})
						}
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 201")})
					case *pgproto3.Sync:
						b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
						if b.Flush() != nil {
							return
						}
					case *pgproto3.Terminate:
						return
					default:
						return
					}
				}
			}()
		}
	}()
	return Config{Protocol: "postgres", Host: "registered-asset.invalid", Database: "fixture", Username: "reader", TLSMode: "disable", Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}}, log
}
func TestPostgresDriverReadOnlyAndRowLimit(t *testing.T) {
	c, log := pgFixture(t)
	result, err := Query(context.Background(), c, "SELECT 1 AS value")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != MaxRows || !result.Truncated {
		t.Fatalf("result rows=%d truncated=%v", len(result.Rows), result.Truncated)
	}
	for _, q := range []string{"BEGIN READ ONLY", "LIMIT 201", "ROLLBACK"} {
		if !log.contains(q) {
			t.Errorf("driver did not send %s", q)
		}
	}
}

func mysqlPacket(c net.Conn, seq byte, p []byte) error {
	h := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), seq}
	if _, err := c.Write(append(h, p...)); err != nil {
		return err
	}
	return nil
}
func readMySQL(c net.Conn) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		return nil, err
	}
	n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
	p := make([]byte, n)
	_, err := io.ReadFull(c, p)
	return p, err
}
func mysqlFixture(t *testing.T) (Config, *queryLog) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	log := &queryLog{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				capabilities := uint32(1 | 8 | 512 | 8192 | 32768 | 131072 | 524288)
				p := append([]byte{10}, []byte("8.0.36\x00")...)
				p = append(p, 1, 0, 0, 0)
				p = append(p, []byte("12345678")...)
				p = append(p, 0, byte(capabilities), byte(capabilities>>8), 45, 2, 0, byte(capabilities>>16), byte(capabilities>>24), 21)
				p = append(p, make([]byte, 10)...)
				p = append(p, []byte("abcdefghijkl\x00mysql_native_password\x00")...)
				if mysqlPacket(c, 0, p) != nil {
					return
				}
				if _, err := readMySQL(c); err != nil {
					return
				}
				ok := []byte{0, 0, 0, 2, 0, 0, 0}
				if mysqlPacket(c, 2, ok) != nil {
					return
				}
				for {
					p, err := readMySQL(c)
					if err != nil || len(p) == 0 || p[0] == 1 {
						return
					}
					if p[0] != 3 {
						return
					}
					q := string(p[1:])
					log.add(q)
					if !strings.HasPrefix(strings.ToUpper(q), "SELECT") {
						if mysqlPacket(c, 1, ok) != nil {
							return
						}
						continue
					}
					mysqlPacket(c, 1, []byte{1})
					col := []byte{3, 'd', 'e', 'f', 0, 0, 0, 5, 'v', 'a', 'l', 'u', 'e', 0, 12, 45, 0}
					size := make([]byte, 4)
					binary.LittleEndian.PutUint32(size, 64)
					col = append(col, size...)
					col = append(col, 253, 0, 0, 0, 0, 0)
					mysqlPacket(c, 2, col)
					mysqlPacket(c, 3, []byte{254, 0, 0, 2, 0})
					mysqlPacket(c, 4, append([]byte{11}, []byte("fixture-row")...))
					if mysqlPacket(c, 5, []byte{254, 0, 0, 2, 0}) != nil {
						return
					}
				}
			}()
		}
	}()
	return Config{Protocol: "mysql", Host: "registered-asset.invalid", Database: "fixture", Username: "reader", TLSMode: "disable", Dial: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}}, log
}
func TestMySQLDriverReadOnlyTransaction(t *testing.T) {
	c, log := mysqlFixture(t)
	result, err := Query(context.Background(), c, "SELECT 1 AS value")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0] != "fixture-row" {
		t.Fatalf("result: %+v", result)
	}
	for _, q := range []string{"MAX_EXECUTION_TIME=5000", "START TRANSACTION READ ONLY", "LIMIT 201", "ROLLBACK"} {
		if !log.contains(q) {
			t.Errorf("driver did not send %s", q)
		}
	}
}

type noDeadlineConn struct{ net.Conn }

func (noDeadlineConn) SetDeadline(time.Time) error      { return errors.New("not supported") }
func (noDeadlineConn) SetReadDeadline(time.Time) error  { return errors.New("not supported") }
func (noDeadlineConn) SetWriteDeadline(time.Time) error { return errors.New("not supported") }
func TestTimeoutClosesSSHLikeConnection(t *testing.T) {
	for _, protocol := range []string{"postgres", "mysql"} {
		t.Run(protocol, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			started := time.Now()
			cfg := Config{Protocol: protocol, Host: "registered.invalid", Database: "db", Username: "reader", TLSMode: "disable", Dial: func(context.Context) (net.Conn, error) {
				client, server := net.Pipe()
				go func() { defer server.Close(); io.Copy(io.Discard, server) }()
				return noDeadlineConn{client}, nil
			}}
			if _, err := Query(ctx, cfg, "SELECT 1"); err == nil {
				t.Fatal("stalled server succeeded")
			}
			if time.Since(started) > time.Second {
				t.Fatal("context did not interrupt deadline-less transport")
			}
		})
	}
}
func TestInvalidQueryNeverDials(t *testing.T) {
	var dialed bool
	_, err := Query(context.Background(), Config{Dial: func(context.Context) (net.Conn, error) { dialed = true; return nil, nil }}, "DELETE FROM users")
	if err == nil || dialed {
		t.Fatal("invalid query reached driver")
	}
}
