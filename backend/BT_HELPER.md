# 宝塔 12 巡检助手（可选，在被管 Linux 上安装）
普通巡检账号先直接读配置。直接读取遇到权限错误时，平台仅执行下面这条命令；sudo 不可用时仍保留分项报告。

    /usr/bin/sudo -n -- /usr/local/libexec/aibot-bt-evidence

用实际的 SSH 巡检账号代替示例中的 `aibot-inspect`。以 root 从已核对的发布包安装：

    install -d -o root -g root -m 0755 /usr/local/libexec
    install -o root -g root -m 0755 backend/aibot-bt-evidence /usr/local/libexec/aibot-bt-evidence
    visudo -f /etc/sudoers.d/aibot-bt-evidence

只写入这条 sudoers 规则，末尾的两个双引号必须保留（仅允许空参数列表）：

    aibot-inspect ALL=(root) NOPASSWD: NOSETENV: /usr/local/libexec/aibot-bt-evidence ""

并检查：

    chown root:root /etc/sudoers.d/aibot-bt-evidence
    chmod 0440 /etc/sudoers.d/aibot-bt-evidence
    visudo -c
    namei -l /usr/local/libexec/aibot-bt-evidence
    sudo -l -U aibot-inspect
    runuser -u aibot-inspect -- /usr/bin/sudo -n -- /usr/local/libexec/aibot-bt-evidence
    runuser -u aibot-inspect -- /usr/bin/sudo -n -- /usr/local/libexec/aibot-bt-evidence /etc/passwd

倒数第二条应输出 JSON（只有 version 和 rules）。最后一条必须被拒绝，无文件内容输出。再以原巡检账号发起一次巡检：无权直接读库时，成功的 bt_rules 项来源为「宝塔只读助手」；可读空表为成功且该表为 []，没有可读的表为「未核实」。不需要给账号授予 python3、sqlite3、shell、iptables 或任意其他 sudo 权限，也不需要放宽数据库权限。

脚本使用固定 /usr/bin/python3 -IS（忽略 Python 环境变量，不启动 site 模块），不接受文件、SQL、环境配置或命令参数。它仅在 /www/server/panel 下读 class/common.py 的版本文本和 data/db/firewall.db 的两个已知表，每表最多 256 行、每行四个映射字段。拒绝符号链接、虚表、视图和计算列；提权时拒绝非 root 所有或组/其他用户可写的输入路径。SQLite 使用只读连接；活跃的 WAL 需要现成的配套共享内存文件。缺少证据时不从 NAT 状态倒推出宝塔配置。若环境不满足这些约束，请保留未核实状态，不要为此降低目录或 DB 的权限。
