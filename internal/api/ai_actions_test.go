package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"server-aibot/internal/ai"
	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/model"
	"server-aibot/internal/operations"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

func aiActionFixture(t *testing.T) (*testEnv, *Server, string, func() string) {
	t.Helper()
	e := newTestEnv(t)
	e.srv.Close()
	key := []byte(strings.Repeat("k", 32))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.Control{MasterKey: key}, e.store, hub.New(e.store, log), log)
	e.srv = httptest.NewServer(s.Routes())
	t.Cleanup(func() { e.srv.Close(); s.StopOperations(); s.CloseAccess(); s.CloseChanges() })
	ctx := context.Background()
	var admin store.Admin
	var session string
	if err := e.store.DB().QueryRow(`SELECT admin_id,id FROM admin_sessions LIMIT 1`).Scan(&admin.ID, &session); err != nil {
		t.Fatal(err)
	}
	admin.Username = "admin"
	if err := e.store.DB().QueryRow(`SELECT password_hash FROM admins WHERE id=?`, admin.ID).Scan(&admin.PasswordHash); err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(key)
	previous := time.Now().Add(-30 * time.Second)
	setup, err := e.store.BeginTOTP(ctx, &admin, session, v, previous)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	otpAt := func(at time.Time) string {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(at.Unix()/30))
		m := hmac.New(sha1.New, seed)
		m.Write(b[:])
		sum := m.Sum(nil)
		i := int(sum[len(sum)-1] & 15)
		return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[i:i+4])&0x7fffffff)%1000000)
	}
	if _, err = e.store.ConfirmTOTP(ctx, &admin, session, otpAt(previous), v, previous); err != nil {
		t.Fatal(err)
	}
	if err = e.store.SetStepUp(ctx, session, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	conversation := seedAI(t, e, "http://127.0.0.1:1/v1")
	return e, s, conversation, func() string { return otpAt(time.Now()) }
}

func proposeAIAction(t *testing.T, s *Server, conversation, name, id string) store.AIProposal {
	t.Helper()
	key := "checkId"
	if name == "propose_forward_stop" {
		key = "forwardId"
	}
	raw, _ := json.Marshal(map[string]string{key: id, "reason": "requested maintenance"})
	var call ai.ToolCall
	call.Function.Name = name
	call.Function.Arguments = string(raw)
	p, err := (&aiRuntime{server: s}).propose(context.Background(), conversation, call)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAIForwardStopApprovalOutcomeAndReplay(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			e, s, conversation, otp := aiActionFixture(t)
			ctx := context.Background()
			d, _, err := e.store.CreateDevice(ctx, "test", "", "")
			if err != nil {
				t.Fatal(err)
			}
			n, _, err := e.store.CreateNode(ctx, model.Node{Name: "test", Addr: "127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			f, err := e.store.CreateForward(ctx, model.Forward{Name: "test", Kind: "port", DeviceID: d.ID, NodeID: n.ID, State: "applied", TargetIP: "127.0.0.1", TargetPort: 80, RemotePort: 18080})
			if err != nil {
				t.Fatal(err)
			}
			p := proposeAIAction(t, s, conversation, "propose_forward_stop", f.ID)
			before, err := e.store.GetForward(ctx, f.ID)
			if err != nil || before.Revision != f.Revision || before.State != "applied" {
				t.Fatal("preview changed forward")
			}
			path := "/api/v1/ai/tool-calls/" + p.ID + "/approve"
			for _, input := range []struct {
				csrf   bool
				body   map[string]any
				status int
			}{
				{false, map[string]any{"confirm": true}, 403},
				{true, map[string]any{"confirm": false}, 400},
				{true, map[string]any{"confirm": true}, 403},
				{true, map[string]any{"confirm": true, "otp": "invalid"}, 403},
			} {
				res := e.do(t, "POST", path, input.body, input.csrf)
				res.Body.Close()
				if res.StatusCode != input.status {
					t.Fatalf("gate: %d", res.StatusCode)
				}
			}
			if stale {
				f, err = e.store.CommandForward(ctx, f.ID, true)
				if err != nil {
					t.Fatal(err)
				}
			}
			res := e.do(t, "POST", path, map[string]any{"confirm": true, "otp": otp()}, true)
			if res.StatusCode != 200 {
				t.Fatalf("approve=%d", res.StatusCode)
			}
			result := decode[store.AIProposal](t, res)
			after, err := e.store.GetForward(ctx, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stale {
				if result.Status != "failed" || after.Revision != f.Revision || after.State != f.State {
					t.Fatalf("stale action executed: %+v %+v", result, after)
				}
			} else {
				if result.Status != "dispatched" || after.State != "stopping" || after.Revision != f.Revision+1 {
					t.Fatalf("stop outcome: %+v %+v", result, after)
				}
				s.handleNodeFrame(n.ID, model.AgentFrame{Type: "revoke_result", ForwardID: f.ID, Revision: after.Revision, OK: true})
				after, err = e.store.GetForward(ctx, f.ID)
				if err != nil || after.State != "stopped" {
					t.Fatal("matching revoke acknowledgement did not finish stop")
				}
			}
			res = e.do(t, "POST", path, map[string]any{"confirm": true, "otp": otp()}, true)
			res.Body.Close()
			if res.StatusCode != 409 {
				t.Fatalf("replay=%d", res.StatusCode)
			}
			final, err := e.store.GetForward(ctx, f.ID)
			if err != nil || final.Revision != after.Revision {
				t.Fatal("replay advanced revision")
			}
		})
	}
}

func TestAIInspectionApprovalSnapshotOutcomeAndReplay(t *testing.T) {
	for _, change := range []string{"none", "check", "asset", "deleted", "runtime"} {
		t.Run(change, func(t *testing.T) {
			e, s, conversation, _ := aiActionFixture(t)
			ctx := context.Background()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { listener.Close() })
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					conn.Close()
				}
			}()
			port := listener.Addr().(*net.TCPAddr).Port
			asset, err := e.store.CreateAsset(ctx, store.Asset{Name: "test", Address: "127.0.0.1", Port: port, Protocol: "tcp", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			check, err := e.store.SaveOperationCheck(ctx, store.OperationCheck{Name: "test", Kind: "tcp", TargetKind: "asset", TargetID: asset.ID, TimeoutMS: 1000, IntervalSeconds: 30}, true)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			s.SetOperationsDialer(operations.DialFunc(func(ctx context.Context, target operations.Target) (net.Conn, error) {
				calls.Add(1)
				if target.ID != asset.ID || target.Port != port {
					t.Error("unexpected inspection target")
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", target.Host, target.Port))
			}))
			p := proposeAIAction(t, s, conversation, "propose_inspection_run", check.ID)
			if calls.Load() != 0 {
				t.Fatal("preview executed check")
			}
			switch change {
			case "check":
				check.TimeoutMS = 2000
				_, err = e.store.SaveOperationCheck(ctx, check, false)
			case "asset":
				asset.Port = 23456
				_, err = e.store.UpdateAsset(ctx, *asset)
			case "deleted":
				err = e.store.DeleteOperationCheck(ctx, check.ID)
			case "runtime":
				_, err = e.store.DB().Exec(`UPDATE operation_checks SET state='down',checked_at=123 WHERE id=?`, check.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/ai/tool-calls/" + p.ID + "/approve"
			// Concurrent HTTP approvals must claim the proposal exactly once.
			var wg sync.WaitGroup
			statuses := make(chan int, 2)
			results := make(chan store.AIProposal, 1)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res := e.do(t, "POST", path, map[string]bool{"confirm": true}, true)
					statuses <- res.StatusCode
					if res.StatusCode == 200 {
						results <- decode[store.AIProposal](t, res)
					} else {
						res.Body.Close()
					}
				}()
			}
			wg.Wait()
			close(statuses)
			counts := map[int]int{}
			for code := range statuses {
				counts[code]++
			}
			if counts[200] != 1 || counts[409] != 1 {
				t.Fatalf("claims=%v", counts)
			}
			result := <-results
			runs, err := e.store.ListInspectionRuns(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if change == "none" || change == "runtime" {
				if calls.Load() != 1 || len(runs) != 1 || runs[0].CheckID != check.ID || runs[0].State != "up" || result.Status != "succeeded" || !strings.Contains(result.Result, runs[0].ID) {
					t.Fatalf("inspection outcome calls=%d runs=%+v result=%+v", calls.Load(), runs, result)
				}
			} else {
				if calls.Load() != 0 || len(runs) != 0 || result.Status != "failed" {
					t.Fatalf("stale check executed: %+v", result)
				}
				if change != "deleted" {
					if _, err = e.store.SaveOperationCheck(ctx, check, false); err != nil {
						t.Fatalf("stale approval left lease: %v", err)
					}
				}
			}
			if len(result.Args) != 0 {
				t.Fatal("internal snapshot leaked")
			}
		})
	}
}

func TestAIInspectionRevalidationHoldsDefinitionLease(t *testing.T) {
	e, s, _, _ := aiActionFixture(t)
	ctx := context.Background()
	check, err := e.store.SaveOperationCheck(ctx, store.OperationCheck{Name: "lease fixture", Kind: "tcp", TimeoutMS: 1000, IntervalSeconds: 30}, true)
	if err != nil {
		t.Fatal(err)
	}
	stale := errors.New("stale approval")
	_, err = s.operationService().RunApproved(ctx, check.ID, func(ctx context.Context, current store.OperationCheck) error {
		current.TimeoutMS = 2000
		if _, err := e.store.SaveOperationCheck(ctx, current, false); !errors.Is(err, store.ErrInUse) {
			t.Errorf("definition edited during revalidation: %v", err)
		}
		return stale
	})
	if !errors.Is(err, stale) {
		t.Fatalf("validation failure lost: %v", err)
	}
	if _, err = e.store.SaveOperationCheck(ctx, check, false); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
	runs, err := e.store.ListInspectionRuns(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatal("rejected snapshot generated a report")
	}
}

func TestAIActionRejectsCommandsAndUnapprovedExecution(t *testing.T) {
	e, s, conversation, _ := aiActionFixture(t)
	_ = e
	for _, name := range []string{"propose_forward_stop", "propose_inspection_run"} {
		action, ok := getAIAction(name)
		if !ok {
			t.Fatal("action not registered")
		}
		for _, raw := range []string{`{"checkId":"x","reason":"test","command":"id"}`, `{"forwardId":"x","reason":"test","command":"id"}`, `{"checkId":"x","reason":"test"} {}`, `{"checkId":"x","reason":" "}`} {
			if _, err := action.preview(context.Background(), s, json.RawMessage(raw)); err == nil {
				t.Fatalf("accepted invalid arguments: %s", raw)
			}
		}
		if _, _, err := action.execute(context.Background(), s, store.AIProposal{ConversationID: conversation}); err == nil {
			t.Fatal("accepted missing approval")
		}
	}
}
