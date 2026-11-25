package operations

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"

	"server-aibot/internal/access"
	"server-aibot/internal/store"
)

type SSHInspector func(context.Context, store.OperationCheck) (string, string, store.InspectionDetail)
type fixedCommand struct{ name, command string }

var sshProfiles = map[string][]fixedCommand{
	"system":   {{"kernel", "uname -sr"}, {"uptime", "uptime"}, {"root_disk", "df -Pk /"}, {"memory", "cat /proc/meminfo"}},
	"services": {{"failed_services", "systemctl --failed --no-legend --plain --no-pager"}},
	"docker":   {{"containers", "docker ps -a --format '{{.Names}}\t{{.Status}}'"}},
	"firewall": {{"rules", "if command -v nft >/dev/null 2>&1; then nft list ruleset; else iptables -S; fi"}},
}

// Commands are selected only from this server-owned allowlist. No user text is
// interpolated into shell commands, environment, paths, or command arguments.
func InspectSSH(ctx context.Context, e *access.Engine, c store.OperationCheck) (string, string, store.InspectionDetail) {
	detail := store.InspectionDetail{Source: "ssh", Profile: c.Profile}
	commands, ok := sshProfiles[c.Profile]
	if !ok {
		return "stale", "unsupported: 未知固定巡检配置", detail
	}
	client, chain, err := e.SSH(ctx, c.TargetID)
	if err != nil {
		return "stale", "SSH 访问失败：检查固定指纹、凭据和登记访问路径", detail
	}
	defer chain.Close()
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close(); chain.Close() })
	defer stop()
	state := "up"
	for index, cmd := range commands {
		evidence := store.InspectionEvidence{Name: cmd.name, State: "up", Summary: "只读命令执行成功"}
		session, err := client.NewSession()
		if err != nil {
			evidence.State = "stale"
			evidence.Summary = "无法创建 SSH 命令会话"
			detail.Evidence = append(detail.Evidence, evidence)
			if state != "down" {
				state = "stale"
			}
			for _, pending := range commands[index+1:] {
				detail.Evidence = append(detail.Evidence, store.InspectionEvidence{Name: pending.name, State: "stale", Summary: "访问中断，未执行"})
			}
			break
		}
		output := &limitedEvidence{limit: 16 << 10}
		session.Stdout = output
		session.Stderr = io.Discard
		err = session.Run(cmd.command)
		session.Close()
		evidence.Output = output.String()
		if output.exceeded {
			evidence.State = "stale"
			evidence.Summary = "证据超过 16 KiB，已截断"
		} else if err != nil {
			evidence.State = "stale"
			evidence.Summary = "只读命令不可用、权限不足或超时"
		} else if (cmd.name == "failed_services" && strings.TrimSpace(evidence.Output) != "") || (cmd.name == "containers" && (strings.Contains(strings.ToLower(evidence.Output), "unhealthy") || strings.Contains(evidence.Output, "Exited"))) {
			evidence.State = "down"
			evidence.Summary = "发现失败服务或异常容器"
		}
		if evidence.State == "down" {
			state = "down"
		} else if evidence.State == "stale" && state != "down" {
			state = "stale"
		}
		detail.Evidence = append(detail.Evidence, evidence)
		if ctx.Err() != nil {
			if state != "down" {
				state = "stale"
			}
			for _, pending := range commands[index+1:] {
				detail.Evidence = append(detail.Evidence, store.InspectionEvidence{Name: pending.name, State: "stale", Summary: "检查超时或已取消，未执行"})
			}
			break
		}
	}
	message := "固定只读巡检完成；system/firewall 证据不等于安全策略合规判断"
	if state == "down" {
		message = "固定只读巡检发现失败服务或异常容器"
	} else if state == "stale" {
		message = "固定只读巡检证据不完整，请查看逐项结果"
	}
	return state, message, detail
}

type limitedEvidence struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedEvidence) Len() int       { return b.buffer.Len() }
func (b *limitedEvidence) String() string { return b.buffer.String() }

func (b *limitedEvidence) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			b.buffer.Write(p[:remaining])
		}
		b.exceeded = true
		return remaining, errors.New("evidence limit exceeded")
	}
	return b.buffer.Write(p)
}
