package api

import (
	"testing"

	"server-aibot/internal/config"
)

func TestOriginAllowed(t *testing.T) {
	s := &Server{cfg: config.Control{PublicHost: "aibot.eugenweb.cn"}}
	tests := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{name: "direct same host", origin: "panel.example.com", host: "panel.example.com", want: true},
		{name: "default https port", origin: "panel.example.com", host: "panel.example.com:443", want: true},
		{name: "default http port", origin: "panel.example.com:80", host: "panel.example.com", want: true},
		{name: "proxied public host", origin: "aibot.eugenweb.cn", host: "127.0.0.1:443", want: true},
		{name: "case insensitive", origin: "AIBOT.eugenweb.cn", host: "127.0.0.1:443", want: true},
		{name: "foreign origin", origin: "evil.example.com", host: "127.0.0.1:443", want: false},
		{name: "non default port", origin: "panel.example.com:8443", host: "panel.example.com", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.originAllowed(tc.origin, tc.host); got != tc.want {
				t.Fatalf("originAllowed(%q, %q) = %v, want %v", tc.origin, tc.host, got, tc.want)
			}
		})
	}
}
