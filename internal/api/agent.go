package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"server-aibot/internal/store"
)

// agentWS authenticates a device by key, then hands the socket to the hub.
// Keys travel in headers, never in the query string, so they cannot leak into
// proxy or access logs.
func (s *Server) agentWS(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.Header.Get("X-Device-ID"))
	auth := r.Header.Get("Authorization")
	key := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if deviceID == "" || key == "" || !strings.HasPrefix(auth, "Bearer ") {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "缺少设备凭据")
		return
	}
	want, err := s.store.DeviceKeyHash(r.Context(), deviceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "设备不存在或凭据无效")
			return
		}
		s.internal(w, "读取设备凭据失败", err)
		return
	}
	got := store.TokenDigest(key)
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		s.store.Audit(r.Context(), "agent:"+deviceID, "agent.connect", deviceID, "", clientIP(r), r.UserAgent(), "denied")
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "设备凭据无效")
		return
	}
	s.store.Audit(r.Context(), "agent:"+deviceID, "agent.connect", deviceID, "", clientIP(r), r.UserAgent(), "ok")
	s.hub.Serve(deviceID, w, r)
}

// nodeWS authenticates a public relay node by its one-time node key. Node
// credentials are separate from device credentials: a compromised agent must
// not be able to register itself as a relay.
func (s *Server) nodeWS(w http.ResponseWriter, r *http.Request) {
	nodeID := strings.TrimSpace(r.Header.Get("X-Node-ID"))
	auth := r.Header.Get("Authorization")
	key := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if nodeID == "" || key == "" || !strings.HasPrefix(auth, "Bearer ") {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "缺少节点凭据")
		return
	}
	want, err := s.store.NodeKeyHash(r.Context(), nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "节点不存在或凭据无效")
			return
		}
		s.internal(w, "读取节点凭据失败", err)
		return
	}
	got := store.TokenDigest(key)
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		s.store.Audit(r.Context(), "node:"+nodeID, "node.connect", nodeID, "", clientIP(r), r.UserAgent(), "denied")
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "节点凭据无效")
		return
	}
	s.store.Audit(r.Context(), "node:"+nodeID, "node.connect", nodeID, "", clientIP(r), r.UserAgent(), "ok")
	s.nodes.Serve(nodeID, w, r)
}
