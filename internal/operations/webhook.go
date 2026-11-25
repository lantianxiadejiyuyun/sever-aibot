package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"server-aibot/internal/store"
)

// ValidateWebhook performs syntax/literal checks without sending any request.
// DNS is resolved again at connection time and the validated IP is pinned.
func ValidateWebhook(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 4096 {
		return errors.New("Webhook 必须是无用户名、无 fragment 的 HTTPS 地址（最多 4096 字节）")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.Contains(host, "metadata") {
		return errors.New("Webhook 不允许本地或 metadata 地址")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicIP(ip) {
		return errors.New("Webhook 不允许内网、保留或 metadata 地址")
	}
	if u.Port() != "" && u.Port() != "443" {
		return errors.New("Webhook 仅允许 HTTPS 443 端口")
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func webhookDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid callback address")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("callback DNS lookup failed")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("callback DNS contains blocked address")
		}
	}
	var last error
	for _, ip := range ips {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	_ = last
	return nil, errors.New("callback connection failed")
}
func (s *Service) deliver(ctx context.Context, d store.NotificationDelivery) string {
	if s.Vault == nil {
		return "加密服务不可用"
	}
	channel, raw, err := s.Store.NotificationChannelSecret(ctx, s.Vault, d.ChannelID)
	if err != nil {
		return "频道不存在或秘密解密失败"
	}
	if !channel.Enabled {
		return "频道已禁用"
	}
	if err = ValidateWebhook(raw); err != nil {
		return "回调地址被安全策略拒绝"
	}
	payload := []byte(d.Payload)
	if channel.Kind == "feishu" {
		var event struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(payload, &event) != nil {
			return "通知载荷无效"
		}
		payload, _ = json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": event.Text}})
	}
	if len(payload) > 64<<10 {
		return "通知载荷过大"
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tr := &http.Transport{DialContext: webhookDial, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects disabled") }}
	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, raw, bytes.NewReader(payload))
	if err != nil {
		return "回调请求无效"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", d.EventID)
	resp, err := client.Do(req)
	if err != nil {
		return "回调请求失败（地址策略、网络或超时），URL 已隐藏"
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return "回调响应读取失败或超过 64 KiB"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Sprintf("回调返回 HTTP %d", resp.StatusCode)
	}
	if channel.Kind == "feishu" {
		var result struct {
			Code       *int `json:"code"`
			StatusCode *int `json:"StatusCode"`
		}
		if json.Unmarshal(body, &result) != nil {
			return "飞书响应格式无效"
		}
		if result.Code != nil {
			if *result.Code != 0 {
				return fmt.Sprintf("飞书返回错误码 %d", *result.Code)
			}
		} else if result.StatusCode != nil {
			if *result.StatusCode != 0 {
				return fmt.Sprintf("飞书返回错误码 %d", *result.StatusCode)
			}
		} else {
			return "飞书响应缺少结果码"
		}
	}
	return ""
}
