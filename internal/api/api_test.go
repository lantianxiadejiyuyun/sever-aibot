package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"server-aibot/internal/config"
	"server-aibot/internal/hub"
	"server-aibot/internal/store"
)

type testEnv struct {
	srv      *httptest.Server
	store    *store.Store
	client   *http.Client
	csrf     string
	password string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	password := randomString(t, 24)
	if _, err := st.EnsureAdmin(context.Background(), "admin", password); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, log)
	s := New(config.Control{MasterKey: key, AdminUser: "admin", AdminPassword: password, SessionTTL: 12}, st, h, log)
	ts := httptest.NewServer(s.Routes())
	t.Cleanup(func() { ts.Close(); s.CloseChanges(); s.StopOperations(); s.CloseAccess() })
	jar, _ := cookiejar.New(nil)
	env := &testEnv{srv: ts, store: st, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}, password: password}
	env.login(t, password)
	return env
}

func randomString(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (e *testEnv) login(t *testing.T, password string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	resp, err := e.client.Post(e.srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var out struct {
		CSRF string `json:"csrfToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.CSRF == "" {
		t.Fatal("login returned an empty csrf token")
	}
	e.csrf = out.CSRF
}

func (e *testEnv) do(t *testing.T, method, path string, body any, csrf bool) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		req.Header.Set("X-CSRF-Token", e.csrf)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestHealthAndAuth(t *testing.T) {
	env := newTestEnv(t)

	resp, err := http.Get(env.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	anon := &http.Client{Timeout: 5 * time.Second}
	r, err := anon.Get(env.srv.URL + "/api/v1/devices")
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated devices status = %d", r.StatusCode)
	}
	r.Body.Close()

	// Missing CSRF must be rejected even with a valid session cookie.
	r = env.do(t, http.MethodPost, "/api/v1/devices", map[string]string{"name": "edge-1"}, false)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d", r.StatusCode)
	}
	r.Body.Close()

	r = env.do(t, http.MethodPost, "/api/v1/devices", map[string]string{"name": "edge-1"}, true)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create device status = %d", r.StatusCode)
	}
	var created struct {
		Device    map[string]any `json:"device"`
		DeviceKey string         `json:"deviceKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if created.DeviceKey == "" {
		t.Fatal("deviceKey missing from creation response")
	}
	for _, forbidden := range []string{"keyHash", "key_hash", "key"} {
		if _, ok := created.Device[forbidden]; ok {
			t.Fatalf("device response leaked %q", forbidden)
		}
	}
}

func TestForwardValidation(t *testing.T) {
	env := newTestEnv(t)

	nodeBody := map[string]any{
		"name": "hk-1", "addr": "node.example:7000", "protocol": "tcp", "forwardPorts": "20000-20010",
		"features": map[string]bool{"web": true, "forward": true, "tunnel": false, "p2p": false, "proxy": false},
	}
	r := env.do(t, http.MethodPost, "/api/v1/nodes", nodeBody, true)
	if r.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("create node status = %d body=%s", r.StatusCode, body)
	}
	var nodeResp struct {
		Node    map[string]any `json:"node"`
		NodeKey string         `json:"nodeKey"`
	}
	json.NewDecoder(r.Body).Decode(&nodeResp)
	r.Body.Close()
	nodeID, _ := nodeResp.Node["id"].(string)
	if nodeID == "" {
		t.Fatal("node id missing")
	}
	if nodeResp.NodeKey == "" {
		t.Fatal("nodeKey missing from creation response")
	}
	if _, leaked := nodeResp.Node["keyHash"]; leaked {
		t.Fatal("node response leaked keyHash")
	}

	deviceResp := env.do(t, http.MethodPost, "/api/v1/devices", map[string]string{"name": "lan-1"}, true)
	var deviceCreated struct {
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
	}
	if err := json.NewDecoder(deviceResp.Body).Decode(&deviceCreated); err != nil {
		t.Fatal(err)
	}
	deviceResp.Body.Close()
	if deviceResp.StatusCode != http.StatusCreated {
		t.Fatalf("create device status = %d", deviceResp.StatusCode)
	}

	portForward := map[string]any{
		"name": "office-rdp", "kind": "port", "nodeId": nodeID, "deviceId": deviceCreated.Device.ID,
		"targetIp": "192.168.1.10", "targetPort": 3389, "remotePort": 20001,
	}
	for _, raw := range []string{`{"relay":null}`, `{"relay":"false"}`, `{"relay":0}`} {
		portForward["options"] = raw
		r = env.do(t, http.MethodPost, "/api/v1/forwards", portForward, true)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid relay option %s status = %d", raw, r.StatusCode)
		}
	}
	delete(portForward, "options")
	r = env.do(t, http.MethodPost, "/api/v1/forwards", portForward, true)
	if r.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("create forward status = %d body=%s", r.StatusCode, body)
	}
	r.Body.Close()

	// Same node+port conflicts.
	r = env.do(t, http.MethodPost, "/api/v1/forwards", portForward, true)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate port status = %d", r.StatusCode)
	}
	r.Body.Close()

	outOfRange := map[string]any{
		"name": "bad-port", "kind": "port", "nodeId": nodeID, "deviceId": deviceCreated.Device.ID,
		"targetIp": "192.168.1.11", "targetPort": 22, "remotePort": 30000,
	}
	r = env.do(t, http.MethodPost, "/api/v1/forwards", outOfRange, true)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("out-of-range port status = %d", r.StatusCode)
	}
	r.Body.Close()

	// A disabled capability rejects the entry type.
	hostForward := map[string]any{
		"name": "nas", "kind": "tunnel", "nodeId": nodeID, "targetIp": "192.168.1.12", "targetPort": 22,
	}
	r = env.do(t, http.MethodPost, "/api/v1/forwards", hostForward, true)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("disabled feature status = %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestAgentHandshakeMarksDeviceOnline(t *testing.T) {
	env := newTestEnv(t)

	r := env.do(t, http.MethodPost, "/api/v1/devices", map[string]string{"name": "lan-1"}, true)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create device status = %d", r.StatusCode)
	}
	var created struct {
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
		DeviceKey string `json:"deviceKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(env.srv.URL, "http") + "/agent/ws"
	header := http.Header{}
	header.Set("X-Device-ID", created.Device.ID)
	header.Set("Authorization", "Bearer "+created.DeviceKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	writeFrame := func(v any) {
		b, _ := json.Marshal(v)
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatalf("write frame: %v", err)
		}
	}
	readFrame := func() map[string]any {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	writeFrame(map[string]any{"type": "hello", "version": "test", "os": "linux", "arch": "amd64"})
	// The control plane may push an empty tunnel config before the hello ack.
	for i := 0; i < 5; i++ {
		got := readFrame()
		if got["type"] == "hello_ack" {
			break
		}
		if i == 4 {
			t.Fatalf("hello ack not received, last type = %v", got["type"])
		}
	}
	writeFrame(map[string]any{"type": "heartbeat", "ts": time.Now().Unix()})
	for {
		got := readFrame()["type"]
		if got == "heartbeat_ack" {
			break
		}
		// Configuration pushes are asynchronous to the heartbeat response.
		if got != "config" {
			t.Fatalf("unexpected frame before heartbeat ack: %v", got)
		}
	}

	r = env.do(t, http.MethodGet, "/api/v1/overview", nil, false)
	var overview struct {
		Counts struct {
			DevicesOnline int `json:"devicesOnline"`
		} `json:"counts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if overview.Counts.DevicesOnline != 1 {
		t.Fatalf("devicesOnline = %d, want 1", overview.Counts.DevicesOnline)
	}
}

func TestCredentialAndAssetRoutes(t *testing.T) {
	env := newTestEnv(t)
	secretValue := "test-credential-value-2026"

	r := env.do(t, http.MethodPost, "/api/v1/credentials", map[string]any{
		"name": "ops-ssh", "kind": "ssh_password", "username": "ops",
		"secret": secretValue, "publicMeta": map[string]any{"port": 22},
	}, true)
	createdBody, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create credential status = %d body=%s", r.StatusCode, createdBody)
	}
	if bytes.Contains(createdBody, []byte(secretValue)) {
		t.Fatal("create response leaked the secret")
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("credential id missing")
	}

	r = env.do(t, http.MethodGet, "/api/v1/credentials", nil, false)
	listBody, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if bytes.Contains(listBody, []byte(secretValue)) {
		t.Fatal("credential list leaked the secret")
	}

	// Reveal needs CSRF and then returns the plaintext to the authenticated,
	// step-up-capable operator.
	r = env.do(t, http.MethodPost, "/api/v1/credentials/"+created.ID+"/reveal", nil, false)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("reveal without csrf status = %d", r.StatusCode)
	}
	r.Body.Close()

	// CSRF alone is not enough: sensitive reads require a recent step-up.
	r = env.do(t, http.MethodPost, "/api/v1/credentials/"+created.ID+"/reveal", nil, true)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("reveal without step-up status = %d", r.StatusCode)
	}
	r.Body.Close()

	r = env.do(t, http.MethodPost, "/api/v1/auth/step-up", map[string]string{"password": env.password}, true)
	if r.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("step-up status = %d body=%s", r.StatusCode, body)
	}
	r.Body.Close()

	r = env.do(t, http.MethodPost, "/api/v1/credentials/"+created.ID+"/reveal", nil, true)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("reveal status = %d", r.StatusCode)
	}
	var revealed struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&revealed); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if revealed.Secret != secretValue {
		t.Fatal("reveal returned the wrong secret")
	}

	// Private keys must parse at import and remain replace-only after step-up.
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	r = env.do(t, http.MethodPost, "/api/v1/credentials", map[string]any{
		"name": "deploy-key", "kind": "ssh_key", "username": "deploy",
		"secret": privatePEM,
	}, true)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create ssh_key status = %d", r.StatusCode)
	}
	var keyCred struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&keyCred); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	r = env.do(t, http.MethodPost, "/api/v1/credentials/"+keyCred.ID+"/reveal", nil, true)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("ssh_key reveal status = %d, want 403", r.StatusCode)
	}
	r.Body.Close()

	// An asset can reference the credential, but the SSH config never embeds it.
	r = env.do(t, http.MethodPost, "/api/v1/assets", map[string]any{
		"name": "db-1", "address": "192.168.1.20", "port": 22, "protocol": "ssh",
		"credentialId": created.ID,
	}, true)
	if r.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("create asset status = %d body=%s", r.StatusCode, body)
	}
	var asset struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&asset); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()

	r = env.do(t, http.MethodGet, "/api/v1/assets/"+asset.ID+"/route", nil, false)
	if r.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("asset route status = %d body=%s", r.StatusCode, body)
	}
	r.Body.Close()

	r = env.do(t, http.MethodGet, "/api/v1/assets/"+asset.ID+"/ssh-config", nil, false)
	config, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("ssh-config status = %d", r.StatusCode)
	}
	if !bytes.Contains(config, []byte("Host ")) {
		t.Fatal("ssh-config does not contain a Host block")
	}
	if bytes.Contains(config, []byte(secretValue)) {
		t.Fatal("ssh-config leaked the credential")
	}
}

func TestSPAFallbackAndAPINotFound(t *testing.T) {
	env := newTestEnv(t)

	// A deep client route must serve the SPA shell so iOS swipe-back and
	// direct links work after a refresh.
	resp, err := http.Get(env.srv.URL + "/devices/dev_missing")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SPA route status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("SPA route content-type = %q", ct)
	}
	if !bytes.Contains(bytes.ToLower(body), []byte("<!doctype html")) {
		t.Fatal("SPA route did not return the HTML shell")
	}

	root, err := http.Get(env.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	root.Body.Close()
	if root.StatusCode != http.StatusOK {
		t.Fatalf("root status = %d (index redirect loop?)", root.StatusCode)
	}

	r := env.do(t, http.MethodGet, "/api/v1/does-not-exist", nil, false)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown API status = %d", r.StatusCode)
	}
	var problem errBody
	if err := json.NewDecoder(r.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if problem.Error.Code != "not_found" {
		t.Fatalf("unknown API code = %q", problem.Error.Code)
	}
}

// TestRelayConfigPush covers the control-plane half of the data plane: node
// and agent sockets receive the applied forward, and a bind result moves the
// row to applied.
func TestRelayConfigPush(t *testing.T) {
	env := newTestEnv(t)

	nodeResp := env.do(t, http.MethodPost, "/api/v1/nodes", map[string]any{
		"name": "edge-1", "addr": "127.0.0.1:7000", "forwardPorts": "20000-20010",
		"features": map[string]bool{"web": true, "forward": true, "tunnel": false, "p2p": false, "proxy": false},
	}, true)
	if nodeResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(nodeResp.Body)
		t.Fatalf("create node status = %d body=%s", nodeResp.StatusCode, body)
	}
	var createdNode struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
		NodeKey string `json:"nodeKey"`
	}
	if err := json.NewDecoder(nodeResp.Body).Decode(&createdNode); err != nil {
		t.Fatal(err)
	}
	nodeResp.Body.Close()

	deviceResp := env.do(t, http.MethodPost, "/api/v1/devices", map[string]string{"name": "lan-1"}, true)
	var createdDevice struct {
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
		DeviceKey string `json:"deviceKey"`
	}
	if err := json.NewDecoder(deviceResp.Body).Decode(&createdDevice); err != nil {
		t.Fatal(err)
	}
	deviceResp.Body.Close()

	fwdResp := env.do(t, http.MethodPost, "/api/v1/forwards", map[string]any{
		"name": "ssh", "kind": "port", "nodeId": createdNode.Node.ID, "deviceId": createdDevice.Device.ID,
		"targetIp": "192.168.1.10", "targetPort": 22, "remotePort": 20001,
	}, true)
	if fwdResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(fwdResp.Body)
		t.Fatalf("create forward status = %d body=%s", fwdResp.StatusCode, body)
	}
	var createdForward struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(fwdResp.Body).Decode(&createdForward); err != nil {
		t.Fatal(err)
	}
	fwdResp.Body.Close()

	applyResp := env.do(t, http.MethodPost, "/api/v1/forwards/"+createdForward.ID+"/apply", nil, true)
	if applyResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(applyResp.Body)
		t.Fatalf("apply status = %d body=%s", applyResp.StatusCode, body)
	}
	var applied struct {
		State     string `json:"state"`
		LastError string `json:"lastError"`
	}
	if err := json.NewDecoder(applyResp.Body).Decode(&applied); err != nil {
		t.Fatal(err)
	}
	applyResp.Body.Close()
	if applied.State != "applying" {
		t.Fatalf("apply state = %q, want applying", applied.State)
	}
	if applied.LastError == "" {
		t.Fatal("expected offline notes in lastError")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodeConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.srv.URL, "http")+"/node/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Node-Id": {createdNode.Node.ID}, "Authorization": {"Bearer " + createdNode.NodeKey}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer nodeConn.Close(websocket.StatusNormalClosure, "")
	nodeConn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","version":"test","tls":false}`))
	nodeConfig := readUntilType(t, ctx, nodeConn, "config")
	var nodeCfg struct {
		TunnelSecret string `json:"tunnelSecret"`
		Forwards     []struct {
			ID string `json:"id"`
		} `json:"forwards"`
	}
	if err := json.Unmarshal(nodeConfig, &nodeCfg); err != nil {
		t.Fatal(err)
	}
	if nodeCfg.TunnelSecret == "" {
		t.Fatal("node config missing tunnelSecret")
	}
	if len(nodeCfg.Forwards) != 1 || nodeCfg.Forwards[0].ID != createdForward.ID {
		t.Fatalf("node config forwards = %+v", nodeCfg.Forwards)
	}

	agentConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.srv.URL, "http")+"/agent/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Device-Id": {createdDevice.Device.ID}, "Authorization": {"Bearer " + createdDevice.DeviceKey}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agentConn.Close(websocket.StatusNormalClosure, "")
	agentConn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","version":"test"}`))
	agentConfig := readUntilType(t, ctx, agentConn, "config")
	var agentCfg struct {
		Nodes []struct {
			NodeID   string `json:"nodeId"`
			Addr     string `json:"addr"`
			Ticket   string `json:"ticket"`
			Forwards []struct {
				ID string `json:"id"`
			} `json:"forwards"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(agentConfig, &agentCfg); err != nil {
		t.Fatal(err)
	}
	if len(agentCfg.Nodes) != 1 || agentCfg.Nodes[0].Addr != "127.0.0.1:7000" || agentCfg.Nodes[0].Ticket == "" {
		t.Fatalf("agent config nodes = %+v", agentCfg.Nodes)
	}
	if len(agentCfg.Nodes[0].Forwards) != 1 || agentCfg.Nodes[0].Forwards[0].ID != createdForward.ID {
		t.Fatalf("agent config forwards = %+v", agentCfg.Nodes[0].Forwards)
	}

	if err := nodeConn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"bind_result","forwardId":"`+createdForward.ID+`","revision":1,"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := env.do(t, http.MethodGet, "/api/v1/forwards/"+createdForward.ID, nil, false)
		var f struct {
			State string `json:"state"`
		}
		json.NewDecoder(r.Body).Decode(&f)
		r.Body.Close()
		if f.State == "applied" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("forward did not reach applied after bind_result")
}

func readUntilType(t *testing.T, ctx context.Context, conn *websocket.Conn, want string) []byte {
	t.Helper()
	for i := 0; i < 10; i++ {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read %s: %v", want, err)
		}
		var frame struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == want {
			return data
		}
	}
	t.Fatalf("frame %q not received", want)
	return nil
}

func TestAgentRejectsBadKey(t *testing.T) {
	env := newTestEnv(t)
	wsURL := "ws" + strings.TrimPrefix(env.srv.URL, "http") + "/agent/ws"
	header := http.Header{}
	header.Set("X-Device-ID", "dev_missing")
	header.Set("Authorization", "Bearer not-a-real-key")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		conn.Close(websocket.StatusNormalClosure, "")
		t.Fatal("expected websocket dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}
