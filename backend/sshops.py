import base64
from contextlib import ExitStack, contextmanager
import hashlib
import hmac
import io
import json
import posixpath
import re
import shlex
import socket
import time

import paramiko
from .store import error

Q = shlex.quote


def read_key(text, passphrase):
    if not isinstance(text, str) or len(text) > 65536:
        error("私钥格式或大小不正确")
    for cls in (paramiko.Ed25519Key, paramiko.ECDSAKey, paramiko.RSAKey):
        try:
            return cls.from_private_key(io.StringIO(text), password=passphrase or None)
        except (ValueError, TypeError, paramiko.SSHException):
            continue
    error("私钥或口令无效；支持 OpenSSH / PEM 格式的 Ed25519、ECDSA、RSA")


def fp(key):
    return "SHA256:" + base64.b64encode(hashlib.sha256(key.asbytes()).digest()).decode().rstrip("=")


@contextmanager
def connect(db, host, purpose="inspect", discover=False):
    with ExitStack() as stack:
        jump_id = host.get("jump_host_id")
        if jump_id:
            jump = db.get("host", jump_id)
            if jump.get("jump_host_id"):
                error("仅支持一层跳板")
            via = stack.enter_context(connect(db, jump))
            sock = via.open_channel("direct-tcpip", (host["address"], host["port"]), ("127.0.0.1", 0), timeout=8)
        else:
            sock = socket.create_connection((host["address"], host["port"]), timeout=8)
        stack.callback(sock.close)
        tr = paramiko.Transport(sock)
        stack.callback(tr.close)
        tr.banner_timeout = tr.auth_timeout = 8
        tr.start_client(timeout=8)
        observed = fp(tr.get_remote_server_key())
        if discover:
            yield observed
            return
        if not hmac.compare_digest(observed, host.get("fingerprint") or ""):
            error("主机指纹未确认或已变化；已在认证前停止", 409)
        binding = host.get("bindings", {}).get(purpose)
        if not binding:
            error("未绑定" + purpose + "凭据", 409)
        cred = db.get("credential", binding["credential_id"])
        if not cred["enabled"] or cred["purpose"] != purpose:
            error("凭据已停用或用途不符", 403)
        secret = db.secret("ssh:" + cred["id"])
        tr.auth_publickey(binding["username"], read_key(secret["private_key"], secret.get("passphrase")))
        yield tr


def run(tr, command, timeout=12, limit=24576):
    """Keep draining after the output cap; bound total wall time and never log raw stderr."""
    until = time.monotonic() + timeout
    with tr.open_session(timeout=8) as ch:
        ch.settimeout(8)
        ch.exec_command(command)
        out = bytearray()
        while time.monotonic() < until:
            if ch.recv_ready():
                buf = ch.recv(8192)
                out.extend(buf[:max(0, limit-len(out))])
            if ch.recv_stderr_ready():
                ch.recv_stderr(8192)
            if ch.exit_status_ready() and not ch.recv_ready() and not ch.recv_stderr_ready():
                return ch.recv_exit_status(), out.decode(errors="replace").strip()
            time.sleep(.01)
    raise TimeoutError("SSH command exceeded deadline")


def health(tr, url):
    if not url:
        return "unverified", "未登记健康检查"
    rc, code = run(tr, "curl --silent --output /dev/null --write-out '%{http_code}' --max-time 6 --proto '=http,https' -- " + Q(url))
    return ("ok" if rc == 0 and re.fullmatch(r"2\d\d",code) else "abnormal"), "HTTP " + (code if code.isdigit() else "无响应")


def compose_base(s):
    return "cd " + Q(s["directory"]) + " && docker compose -f " + Q(s["config_path"])


def live(tr, s):
    rc, cid = run(tr, compose_base(s) + " ps -q -- " + Q(s["compose_service"]))
    if rc or not re.fullmatch(r"[a-f0-9]{12,64}", cid):
        error("未找到唯一运行中的 Compose 服务，无法核对现场",409)
    template='{{.Config.Image}}\n{{.Image}}\n{{.State.Running}}\n{{json .NetworkSettings.Ports}}\n{{json (index .Config.Labels "com.docker.compose.project.working_dir")}}\n{{json (index .Config.Labels "com.docker.compose.project.config_files")}}'
    rc, raw = run(tr, "docker inspect --format " + Q(template) + " " + cid)
    parts = raw.splitlines()
    if rc or len(parts)!=6:
        error("读取部署现场失败",409)
    ports = json.loads(parts[3]) or {}
    return {"image":parts[0],"image_id":parts[1],"running":parts[2]=="true","container":cid,
            "directory":json.loads(parts[4]) or None,"config_files":list(filter(None,(json.loads(parts[5]) or "").split(","))),
            "ports":sorted({int(bind["HostPort"]) for binds in ports.values() for bind in (binds or [])})}


def target_command(s,image):
    return "cd "+Q(s["directory"])+" && env "+s["image_variable"]+"="+Q(image)+" docker compose -f "+Q(s["config_path"])


def target_hash(tr,s,image):
    pick="import hashlib,json,sys;c=json.load(sys.stdin);s=c.pop('services')[sys.argv[1]];print(hashlib.sha256(json.dumps([c,s],sort_keys=True).encode()).hexdigest() if s.get('image')==sys.argv[2] else '')"
    rc, value=run(tr,target_command(s,image)+" config --format json | /usr/bin/python3 -I -c "+Q(pick)+" "+Q(s["compose_service"])+" "+Q(image))
    if rc or not re.fullmatch(r"[a-f0-9]{64}",value):
        error("无法核实目标镜像与有效 Compose 配置",409)
    return value


def deploy(tr, s, image, before, step, config_hash):
    if live(tr,s) != before:
        error("部署现场已变化，需要重新预览",409)
    prefix=target_command(s,image)
    if target_hash(tr,s,image)!=config_hash:
        error("Compose 配置已变化，需要重新预览",409)
    step("已复核现场与目标镜像；开始拉取")
    rc,_ = run(tr, prefix + " pull -- " + Q(s["compose_service"]), timeout=180)
    if rc:
        error("镜像拉取失败")
    step("已拉取镜像；开始更新指定服务")
    rc,_ = run(tr, prefix + " up -d --no-deps -- " + Q(s["compose_service"]), timeout=120)
    if rc:
        error("Compose 更新失败；请核对现场")
    step("已执行更新；等待健康检查")
    for _ in range(8):
        current = live(tr,s)
        rc, image_id = run(tr, "docker image inspect --format '{{.Id}}' -- " + Q(image))
        status, ev = health(tr,s["health_url"])
        if (not rc and image_id == current["image_id"] and current["image"] == image and current["running"]
                and posixpath.normpath(s["directory"])==current.get("directory")
                and posixpath.normpath(posixpath.join(s["directory"],s["config_path"])) in current.get("config_files",[])
                and set(s["ports"]) <= set(current["ports"]) and status=="ok"):
            return {**current,"health":ev}
        time.sleep(1)
    error("版本、端口或应用健康检查未通过；保留上次成功档案")
