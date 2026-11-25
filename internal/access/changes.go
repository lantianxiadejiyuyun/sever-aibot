package access

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

const MaxConfigBytes = 64 << 10

type ChangePlan struct {
	AssetID       string `json:"assetId"`
	Kind          string `json:"kind"`
	Service       string `json:"service"`
	ConfigPath    string `json:"configPath,omitempty"`
	Content       string `json:"content,omitempty"`
	BeforeContent string `json:"beforeContent,omitempty"`
	BeforeExists  bool   `json:"beforeExists"`
	BeforeHash    string `json:"beforeHash"`
	RouteHash     string `json:"routeHash"`
}
type ChangeResult struct {
	Status     string `json:"status"`
	Output     string `json:"output"`
	BackupPath string `json:"backupPath,omitempty"`
}
type servicePolicy struct{ path, parent, validate, restart, verify string }

var changeServices = map[string]servicePolicy{
	"nginx": {path: "/etc/nginx/conf.d/aibot-managed.conf", parent: "/etc/nginx/conf.d", validate: "/usr/sbin/nginx -t", restart: "/usr/bin/systemctl restart nginx", verify: "/usr/bin/systemctl is-active --quiet nginx"},
	"caddy": {path: "/etc/caddy/Caddyfile", parent: "/etc/caddy", validate: "/usr/bin/caddy validate --config /etc/caddy/Caddyfile", restart: "/usr/bin/systemctl restart caddy", verify: "/usr/bin/systemctl is-active --quiet caddy"},
}

func ValidateChangePlan(p ChangePlan) error {
	policy, ok := changeServices[p.Service]
	if !ok {
		return errors.New("only nginx and caddy services are allowed")
	}
	if p.Kind != "service_restart" && p.Kind != "config_update" {
		return errors.New("unsupported fixed change kind")
	}
	if p.Kind == "config_update" {
		if p.ConfigPath != policy.path {
			return errors.New("configuration path is not allowlisted for service")
		}
		if len(p.Content) == 0 || len(p.Content) > MaxConfigBytes || strings.ContainsRune(p.Content, 0) {
			return errors.New("configuration must contain 1-65536 non-NUL bytes")
		}
	}
	return nil
}
func digest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}
func (e *Engine) ChangeRouteHash(ctx context.Context, id string) (string, error) {
	chain, err := e.chain(ctx, id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, a := range chain {
		if a.Protocol != "ssh" || a.HostKey == "" {
			return "", errors.New("change route requires pinned SSH assets")
		}
		credentialRevision, err := e.Store.CredentialAccessRevision(ctx, a.CredentialID)
		if err != nil {
			return "", errors.New("change route requires valid bound SSH credentials")
		}
		fmt.Fprintf(&b, "%s|%s|%d|%s|%s|%s|%s|%s|%s|%s\n", a.ID, a.Address, a.Port, a.HostKey, a.CredentialID, a.ViaAssetID, a.ViaDeviceID, a.UpdatedAt, a.Tags, credentialRevision)
	}
	return digest(b.String()), nil
}
func (e *Engine) PreviewChange(ctx context.Context, p ChangePlan) (ChangePlan, error) {
	if err := ValidateChangePlan(p); err != nil {
		return p, err
	}
	hash, err := e.ChangeRouteHash(ctx, p.AssetID)
	if err != nil {
		return p, err
	}
	p.RouteHash = hash
	if p.Kind == "service_restart" {
		return p, nil
	}
	client, chain, err := e.SSH(ctx, p.AssetID)
	if err != nil {
		return p, err
	}
	defer client.Close()
	defer chain.Close()
	exists, content, err := readConfig(ctx, client, changeServices[p.Service])
	if err != nil {
		return p, err
	}
	p.BeforeExists = exists
	p.BeforeContent = content
	p.BeforeHash = digest(content)
	return p, nil
}
func shellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func readConfig(ctx context.Context, client *ssh.Client, p servicePolicy) (bool, string, error) {
	script := fmt.Sprintf("set -eu\n[ ! -L %s ]\n[ \"$(/usr/bin/readlink -f %s)\" = %s ]\nif [ -e %s ]; then [ -f %s ]; [ \"$(/usr/bin/wc -c < %s)\" -le %d ]; /usr/bin/base64 < %s; else printf AIBOT_ABSENT; fi", shellLiteral(p.path), shellLiteral(p.parent), shellLiteral(p.parent), shellLiteral(p.path), shellLiteral(p.path), shellLiteral(p.path), MaxConfigBytes, shellLiteral(p.path))
	out, err := fixedCommand(ctx, client, script, nil, 96<<10)
	if err != nil {
		return false, "", errors.New("cannot read allowlisted configuration snapshot")
	}
	if out == "AIBOT_ABSENT" {
		return false, "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil || len(raw) > MaxConfigBytes {
		return false, "", errors.New("invalid configuration snapshot")
	}
	return true, string(raw), nil
}

// fixedCommand is intentionally unexported; callers only use the fixed change
// policies above. Content is sent on stdin and never interpolated into a command.
func fixedCommand(ctx context.Context, client *ssh.Client, script string, input []byte, limit int) (string, error) {
	stopClient := context.AfterFunc(ctx, func() { client.Close() })
	defer stopClient()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close(); client.Close() })
	defer stop()
	out := &boundedChangeOutput{limit: limit}
	session.Stdout = out
	session.Stderr = out
	session.Stdin = bytes.NewReader(input)
	err = session.Run("sudo -n /bin/sh -c " + shellLiteral(script))
	return out.String(), err
}

type boundedChangeOutput struct {
	buffer bytes.Buffer
	limit  int
	mu     sync.Mutex
}

func (b *boundedChangeOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
func (b *boundedChangeOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return n, nil
}
func configScript(p ChangePlan, policy servicePolicy, backup string) string {
	exists := "0"
	if p.BeforeExists {
		exists = "1"
	}
	return fmt.Sprintf(`set -eu
path=%s
parent=%s
backup=%s
[ ! -L "$path" ] || exit 40
[ "$(/usr/bin/readlink -f "$parent")" = "$parent" ] || exit 40
[ ! -e "$backup" ] && [ ! -L "$backup" ] || exit 40
existed=%s
if [ "$existed" = 1 ]; then
 [ -f "$path" ] || exit 40
 actual=$(/usr/bin/sha256sum "$path"); actual=${actual%%%% *}
 [ "$actual" = %s ] || exit 40
 /bin/cp -p -- "$path" "$backup"
else
 [ ! -e "$path" ] || exit 40
fi
tmp=$(/usr/bin/mktemp "$parent/.aibot-change.XXXXXX")
changed=0
finish() {
 code=$?
 trap - EXIT
 /bin/rm -f -- "$tmp"
 if [ "$code" != 0 ] && [ "$changed" = 1 ]; then
  restored=1
  if [ "$existed" = 1 ]; then /bin/cp -p -- "$backup" "$path" || restored=0; else /bin/rm -f -- "$path" || restored=0; fi
  if [ "$restored" = 1 ] && %s && %s && %s; then printf '\nAIBOT_RESULT=rolled_back\n'; exit 30; else printf '\nAIBOT_RESULT=rollback_failed\n'; exit 31; fi
 fi
 exit "$code"
}
trap finish EXIT
trap 'exit 41' HUP INT TERM
/usr/bin/base64 -d > "$tmp"
if [ "$existed" = 1 ]; then /bin/chmod --reference="$path" "$tmp"; /bin/chown --reference="$path" "$tmp"; else /bin/chmod 0644 "$tmp"; fi
changed=1
/bin/mv -- "$tmp" "$path"
%s
%s
%s
printf '\nAIBOT_RESULT=succeeded\n'
`, shellLiteral(policy.path), shellLiteral(policy.parent), shellLiteral(backup), exists, shellLiteral(p.BeforeHash), policy.validate, policy.restart, policy.verify, policy.validate, policy.restart, policy.verify)
}
func (e *Engine) ExecuteChange(ctx context.Context, p ChangePlan, executionID string) (ChangeResult, error) {
	if err := ValidateChangePlan(p); err != nil {
		return ChangeResult{}, err
	}
	for _, c := range executionID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ChangeResult{}, errors.New("invalid execution ID")
		}
	}
	if executionID == "" {
		return ChangeResult{}, errors.New("execution ID required")
	}
	hash, err := e.ChangeRouteHash(ctx, p.AssetID)
	if err != nil {
		return ChangeResult{}, err
	}
	if hash != p.RouteHash {
		return ChangeResult{}, errors.New("asset route changed since proposal")
	}
	client, chain, err := e.SSH(ctx, p.AssetID)
	if err != nil {
		return ChangeResult{}, err
	}
	defer client.Close()
	defer chain.Close()
	connectedHash, err := e.ChangeRouteHash(ctx, p.AssetID)
	if err != nil {
		return ChangeResult{}, err
	}
	if connectedHash != p.RouteHash {
		return ChangeResult{}, errors.New("asset route or credential changed during connection")
	}
	policy := changeServices[p.Service]
	result := ChangeResult{Status: "failed"}
	script := policy.restart + "\n" + policy.verify
	var input []byte
	if p.Kind == "config_update" {
		exists, content, err := readConfig(ctx, client, policy)
		if err != nil {
			return result, err
		}
		if exists != p.BeforeExists || digest(content) != p.BeforeHash {
			return result, errors.New("configuration changed since proposal")
		}
		backup := policy.path + ".aibot-" + executionID + ".bak"
		if p.BeforeExists {
			result.BackupPath = backup
		}
		script = configScript(p, policy, backup)
		input = []byte(base64.StdEncoding.EncodeToString([]byte(p.Content)))
	} else {
		script = "set -eu\n" + script
	}
	output, err := fixedCommand(ctx, client, script, input, 16<<10)
	result.Output = output
	if err == nil {
		result.Status = "succeeded"
		return result, nil
	}
	if ctx.Err() != nil {
		result.Status = "interrupted"
		return result, ctx.Err()
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitStatus() {
		case 30:
			result.Status = "rolled_back"
		case 31:
			result.Status = "rollback_failed"
		case 40:
			result.Status = "conflict"
		}
	}
	return result, errors.New("fixed SSH change did not complete successfully")
}
