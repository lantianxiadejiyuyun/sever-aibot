package agent

import "testing"

func TestEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://cp.example.com", "wss://cp.example.com/agent/ws", false},
		{"http://127.0.0.1:7400", "ws://127.0.0.1:7400/agent/ws", false},
		{"https://cp.example.com/", "wss://cp.example.com/agent/ws", false},
		{"wss://cp.example.com", "wss://cp.example.com/agent/ws", false},
		{"ws://cp.example.com:8080", "ws://cp.example.com:8080/agent/ws", false},
		{"https://cp.example.com/base/", "wss://cp.example.com/base/agent/ws", false},
		{"https://user:pass@cp.example.com", "wss://cp.example.com/agent/ws", false},
		{"https://cp.example.com?token=secret", "wss://cp.example.com/agent/ws", false},
		{"ftp://cp.example.com", "", true},
		{"cp.example.com", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := Endpoint(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Endpoint(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Endpoint(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Endpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	if got, want := RedactURL("https://user:pass@cp.example.com:7400/x"), "https://cp.example.com:7400/x"; got != want {
		t.Errorf("RedactURL = %q, want %q", got, want)
	}
	if got := RedactURL("http://[::1"); got != "(invalid URL)" {
		t.Errorf("RedactURL(invalid) = %q, want (invalid URL)", got)
	}
}
