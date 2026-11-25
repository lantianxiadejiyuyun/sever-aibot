package operations

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"server-aibot/internal/store"
	"server-aibot/internal/vault"
)

func fixture(t *testing.T) (*Service, store.OperationCheck) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, v, nil)
	a, err := st.CreateAsset(context.Background(), store.Asset{Name: "registered service", Address: "service.example", Port: 443, Protocol: "tcp", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.SaveOperationCheck(context.Background(), store.OperationCheck{Name: "probe", Kind: "tcp", TargetKind: "asset", TargetID: a.ID, TimeoutMS: 1000, IntervalSeconds: 30, Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	return svc, c
}
func successDial(ctx context.Context, t Target) (net.Conn, error) {
	a, b := net.Pipe()
	go b.Close()
	return a, nil
}
func TestInspectionTransitionsAndDurableOutbox(t *testing.T) {
	svc, c := fixture(t)
	ctx := context.Background()
	ch, err := svc.Store.SaveNotificationChannel(ctx, svc.Vault, store.NotificationChannel{Name: "bot", Kind: "webhook", Enabled: true}, "https://hooks.example/secret-token", true)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetDialer(DialFunc(func(context.Context, Target) (net.Conn, error) { return nil, errors.New("connection refused") }))
	for i := 0; i < 2; i++ {
		run, err := svc.Run(ctx, c.ID)
		if err != nil || run.State != "down" {
			t.Fatalf("run=%+v err=%v", run, err)
		}
	}
	out, err := svc.Store.ListNotificationOutbox(ctx)
	if err != nil || len(out) != 1 {
		t.Fatalf("dedup outbox=%+v err=%v", out, err)
	}
	if out[0].ChannelID != ch.ID {
		t.Fatal("wrong channel")
	}
	alerts, err := svc.Store.ListOperationAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].Resolved {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
	if err = svc.Store.AcknowledgeOperationAlert(ctx, alerts[0].ID); err != nil {
		t.Fatal(err)
	}
	// Silence suppresses recovery delivery while preserving the report and resolution.
	if err = svc.Store.SilenceOperationAlert(ctx, alerts[0].ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	svc.SetDialer(DialFunc(successDial))
	if _, err = svc.Run(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	alerts, _ = svc.Store.ListOperationAlerts(ctx)
	if !alerts[0].Resolved || !alerts[0].Acknowledged {
		t.Fatalf("alert not resolved: %+v", alerts)
	}
	out, _ = svc.Store.ListNotificationOutbox(ctx)
	if len(out) != 1 {
		t.Fatal("silenced transition enqueued")
	}
	runs, _ := svc.Store.ListInspectionRuns(ctx)
	if len(runs) != 3 {
		t.Fatal("missing reports")
	}
	// A new failure after recovery creates a fresh incident and event.
	svc.SetDialer(DialFunc(func(context.Context, Target) (net.Conn, error) { return nil, errors.New("down") }))
	if _, err = svc.Run(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	out, _ = svc.Store.ListNotificationOutbox(ctx)
	if len(out) != 2 {
		t.Fatal("new incident missing delivery")
	}
}
func TestMissingAccessAndConcurrentClaim(t *testing.T) {
	svc, c := fixture(t)
	ctx := context.Background()
	run, err := svc.Run(ctx, c.ID)
	if err != nil || run.State != "stale" {
		t.Fatalf("missing access must be stale: %+v %v", run, err)
	}
	claimed, err := svc.Store.ClaimOperationCheck(ctx, c.ID, true)
	if err != nil || !claimed {
		t.Fatal("first claim failed", err)
	}
	if _, err = svc.Run(ctx, c.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("second execution accepted: %v", err)
	}
	if _, err = svc.Store.SaveOperationCheck(ctx, c, false); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("edited leased check: %v", err)
	}
}
func TestRetryLeaseAndSecretRedaction(t *testing.T) {
	svc, c := fixture(t)
	ctx := context.Background()
	secret := "https://hooks.example/highly-secret-token"
	channel, err := svc.Store.SaveNotificationChannel(ctx, svc.Vault, store.NotificationChannel{Name: "bot", Kind: "webhook", Enabled: true}, secret, true)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err = svc.Store.DB().QueryRow(`SELECT secret_ct FROM notification_channels WHERE id=?`, channel.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "highly-secret-token") {
		t.Fatal("plaintext persisted")
	}
	channels, _ := svc.Store.ListNotificationChannels(ctx)
	raw, _ := json.Marshal(channels)
	if strings.Contains(string(raw), "hooks.example") || strings.Contains(string(raw), "secret") {
		t.Fatal("list exposes secret")
	}
	_, opened, err := svc.Store.NotificationChannelSecret(ctx, svc.Vault, channel.ID)
	if err != nil || opened != secret {
		t.Fatal("encrypted URL roundtrip failed")
	}
	if _, err = svc.Run(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	out, _ := svc.Store.ListNotificationOutbox(ctx)
	if len(out) != 1 {
		t.Fatal("missing delivery")
	}
	d := out[0]
	for attempt := 0; attempt < 5; attempt++ {
		// Move only this test record to its due time, without sleeping through backoff.
		if _, err = svc.Store.DB().Exec(`UPDATE notification_outbox SET next_attempt=0 WHERE id=?`, d.ID); err != nil {
			t.Fatal(err)
		}
		ok, err := svc.Store.ClaimNotificationDelivery(ctx, d.ID)
		if err != nil || !ok {
			t.Fatal("claim failed", err)
		}
		ok, err = svc.Store.ClaimNotificationDelivery(ctx, d.ID)
		if err != nil || ok {
			t.Fatal("duplicate lease", err)
		}
		d.Attempts = attempt
		if err = svc.Store.FinishNotificationDelivery(ctx, d, "simulated failure"); err != nil {
			t.Fatal(err)
		}
	}
	out, _ = svc.Store.ListNotificationOutbox(ctx)
	if out[0].State != "failed" || out[0].Attempts != 5 {
		t.Fatalf("wrong retry state: %+v", out)
	}
	logs, _ := svc.Store.ListNotificationLogs(ctx)
	if len(logs) != 5 {
		t.Fatal("missing attempt logs")
	}
	if err = svc.Store.RetryNotificationDelivery(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	out, _ = svc.Store.DueNotificationOutbox(ctx)
	if len(out) != 1 || out[0].Attempts != 0 {
		t.Fatal("manual retry not due")
	}
}
func TestHTTPProbeUsesInjectedRouteAndBounds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://metadata.google.internal/", 302)
		case "/large":
			w.Write(make([]byte, (1<<20)+1))
		case "/error":
			w.WriteHeader(503)
		default:
			w.Write([]byte("ok"))
		}
	}))
	defer server.Close()
	dial := DialFunc(func(ctx context.Context, target Target) (net.Conn, error) {
		if target.ID != "registered" {
			t.Error("lost target identity")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(server.URL, "http://"))
	})
	target := Target{Kind: "asset", ID: "registered", Host: "registered.example", Port: 80}
	for _, test := range []struct {
		path string
		fail bool
	}{{"/", false}, {"/redirect", true}, {"/large", true}, {"/error", true}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := probe(ctx, dial, target, store.OperationCheck{Kind: "http", Scheme: "http", Path: test.path, TimeoutMS: 1000})
		cancel()
		if (err != nil) != test.fail {
			t.Fatalf("%s err=%v", test.path, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("redirect was followed")
	}
}
func TestWebhookRejectsMetadataAndPrivateAddresses(t *testing.T) {
	for _, raw := range []string{"http://example.com/hook", "https://127.0.0.1/hook", "https://169.254.169.254/latest/meta-data", "https://[::1]/", "https://[::ffff:127.0.0.1]/", "https://100.100.100.200/", "https://192.168.1.1/", "https://metadata.google.internal/", "https://example.com:8443/hook", "https://user:secret@example.com/hook"} {
		if ValidateWebhook(raw) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if err := ValidateWebhook("https://open.feishu.cn/open-apis/bot/v2/hook/example"); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "100.100.100.200", "::ffff:10.0.0.1", "fd00:ec2::254", "64:ff9b::a00:1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("allowed IP %s", ip)
		}
	}
	if !publicIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("rejected public IP")
	}
	// Loopback is rejected after resolution too; no connection is attempted.
	if _, err := webhookDial(context.Background(), "tcp", "127.0.0.1:443"); err == nil {
		t.Fatal("dial allowed loopback")
	}
}
func TestScheduleHonorsDueTime(t *testing.T) {
	svc, c := fixture(t)
	svc.SetDialer(DialFunc(successDial))
	ctx := context.Background()
	if _, err := svc.Run(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.Store.ClaimOperationCheck(ctx, c.ID, false); err != nil || ok {
		t.Fatalf("scheduled before due: %v %v", ok, err)
	}
	if _, err := svc.Store.DB().Exec(`UPDATE operation_checks SET next_run=0 WHERE id=?`, c.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := svc.Store.ClaimOperationCheck(ctx, c.ID, false); err != nil || !ok {
		t.Fatalf("did not claim due: %v %v", ok, err)
	}
}
