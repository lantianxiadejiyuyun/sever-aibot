package node

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"server-aibot/internal/model"
	"strconv"
	"strings"
	"time"
)

func (r *Runtime) serveHost(w http.ResponseWriter, req *http.Request) {
	host := strings.ToLower(req.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	var selected model.TunnelForward
	r.mu.Lock()
	for _, f := range r.forwards {
		if f.Kind != "host" {
			continue
		}
		domain := strings.ToLower(f.Domain)
		if domain == host {
			selected = f
			break
		}
		if strings.HasPrefix(domain, "*.") && strings.HasSuffix(host, domain[1:]) && len(domain) > len(selected.Domain) {
			selected = f
		}
	}
	r.mu.Unlock()
	if selected.ID == "" {
		http.NotFound(w, req)
		return
	}
	transport := &http.Transport{
		DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			a, b := net.Pipe()
			go r.handleForwardEntry(b, selected)
			return a, nil
		},
	}
	defer transport.CloseIdleConnections()
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: "forward-target"})
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		http.Error(w, "forward target unavailable", http.StatusBadGateway)
	}
	// Forwarded identity from public callers is untrusted; ReverseProxy adds the
	// actual client to X-Forwarded-For after removing their supplied value.
	req.Header.Del("Forwarded")
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")
	proxy.ServeHTTP(w, req)
}

// reconcileHosts is called under applyMu. Host routing reads the current map
// for each request, so unrelated domain changes keep their HTTP listener.
func (r *Runtime) reconcileHosts(port int, forwards map[string]model.TunnelForward) {
	hosts := []model.TunnelForward{}
	for _, f := range forwards {
		if f.Kind == "host" {
			hosts = append(hosts, f)
		}
	}
	if r.hostServer != nil && (len(hosts) == 0 || port != r.hostPort) {
		_ = r.hostServer.Close()
		r.hostServer = nil
	}
	var err error
	if len(hosts) > 0 && r.hostServer == nil {
		var ln net.Listener
		ln, err = net.Listen("tcp", net.JoinHostPort(r.entryHost, strconv.Itoa(port)))
		if err == nil {
			r.hostPort = port
			r.hostServer = &http.Server{Handler: http.HandlerFunc(r.serveHost), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
			srv := r.hostServer
			r.wg.Add(1)
			go func() { defer r.wg.Done(); _ = srv.Serve(ln) }()
		}
	}
	for _, f := range hosts {
		message := ""
		if err != nil {
			message = err.Error()
		}
		r.sendFrame(model.AgentFrame{Type: "bind_result", ForwardID: f.ID, Revision: f.Revision, OK: err == nil, Message: message})
	}
}
