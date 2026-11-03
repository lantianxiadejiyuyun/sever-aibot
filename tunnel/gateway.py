"""Internal adapter. The manager is the sole owner of desired tunnel configuration."""
import hashlib
import ipaddress
import json
import os
import re
import uuid
from urllib import error, parse, request


class _NoRedirect(request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


class Gateway:
    def __init__(self):
        self.url = os.getenv("GOSTC_CONTROL_URL", "http://127.0.0.1:18081").rstrip("/")
        self.secret = os.getenv("GOSTC_CONTROL_SECRET", "")

    def _post(self, kind, action, data):
        if len(self.secret) < 32:
            raise RuntimeError("未配置 GOSTC_CONTROL_SECRET，不能连接转发控制服务")
        if kind not in ("node", "client", "forward", "p2p"):
            raise RuntimeError("不支持的规则类型")
        url = parse.urlsplit(self.url)
        if url.username or url.password or url.query or url.fragment or not (
            url.scheme == "https" or url.scheme == "http" and url.hostname in ("127.0.0.1", "localhost", "::1")
        ):
            raise RuntimeError("转发控制端须使用本机 HTTP 或已验证的 HTTPS")
        req = request.Request(f"{self.url}/control/{kind}/{action}",
                              json.dumps(data).encode(), {"Content-Type": "application/json",
                              "X-Control-Secret": self.secret})
        try:
            with request.build_opener(request.ProxyHandler({}), _NoRedirect()).open(req, timeout=16) as response:
                body = json.load(response)
        except (error.URLError, TimeoutError, ValueError, OSError):
            raise RuntimeError("转发控制端不可用或身份验证失败") from None
        if body.get("code") != 0:
            raise RuntimeError(str(body.get("msg", "操作失败"))[:240])
        return body.get("data")

    def _rows(self, kind):
        n = 1
        while True:
            page = self._post(kind, "page", {"size": 100, "page": n}) or {}
            rows = page.get("list") or []
            yield from rows
            if not rows or n * 100 >= page.get("total", 0):
                return
            n += 1

    def _find(self, kind, key):
        for row in self._rows(kind):
            if row.get("code") == key:
                return row
        raise RuntimeError("指定编号的对象不存在")

    def _public(self, kind, r):
        out = {"id": r["code"], "name": r.get("name", "")}
        if kind in ("node", "client"):
            if kind == "node":
                out.update(address=r.get("address"), forward_ports=r.get("forwardPorts"),
                           disable_relay=r.get("p2pDisableForward") == 1)
        else:
            node, client = r.get("node") or {}, r.get("client") or {}
            out.update(node_id=node.get("code"), client_id=client.get("code"),
                       target_host=r.get("targetIp"), target_port=int(r["targetPort"]),
                       enabled=r.get("enable") == 1)
            if kind == "forward":
                out.update(public_port=int(r["port"]), protocols=["tcp", "udp"])
            else:
                out["allow_relay"] = r.get("forward") == 1
        out["version"] = hashlib.sha256(json.dumps([out, r.get("revision")], sort_keys=True).encode()).hexdigest()[:20]
        if kind in ("node", "client"):
            out["online"] = r.get("online") == 1
        else:
            out.update(node_online=node.get("online") == 1, client_online=client.get("online") == 1,
                       applied=r.get("applied", "pending"), applied_at=r.get("appliedAt") or None,
                       warning=r.get("warnMsg", ""), public_host=node.get("address"),
                       path="unknown", external_verification="unverified")
        return out

    def call(self, kind, action, data=None, identity=None):
        data = dict(data or {})
        key = str(data.get("id") or data.get("code") or identity or "")
        if action == "list":
            return {"status": "observed", "data": [self._public(kind, r) for r in self._rows(kind)]}
        prev = {}
        if action != "create":
            if not key:
                raise RuntimeError("缺少规则编号")
            prev = self._public(kind, self._find(kind, key))
            if action == "check":
                return {"data": prev, "status": "observed"}
            if data.get("version") and data["version"] != prev["version"]:
                raise RuntimeError("规则已变更，请重新确认")
        else:
            key = str(uuid.uuid4())
        cfg = {**prev, **data}
        wire = {"code": key} if action != "create" else {"id": key}
        if action in ("create", "update"):
            name = str(cfg.get("name", "")).strip()
            if not name or len(name) > 120:
                raise RuntimeError("需要有效名称")
            wire["name"] = name
            if kind in ("forward", "p2p"):
                try:
                    host = str(ipaddress.ip_address(cfg["target_host"]))
                    if ":" in host:
                        raise ValueError
                except (KeyError, ValueError):
                    raise RuntimeError("目标应为客户端可达的 IPv4 地址") from None
                wire.update(targetIp=host, targetPort=_port(cfg.get("target_port")), useCompression=1,
                            useEncryption=1, nodeCode=cfg.get("node_id"), clientCode=cfg.get("client_id"))
                if not wire["nodeCode"] or not wire["clientCode"]:
                    raise RuntimeError("需要节点和客户端编号")
                if prev and (cfg["node_id"] != prev["node_id"] or cfg["client_id"] != prev["client_id"]):
                    raise RuntimeError("不能修改规则所属节点或客户端，请新建映射")
                if kind == "forward":
                    wire["port"] = _port(cfg.get("public_port"))
                else:
                    wire["forward"] = 1 if cfg.get("allow_relay") is True else 2
            elif kind == "node":
                addr = str(cfg.get("address", ""))
                host, sep, port = addr.rpartition(":")
                if not sep or not re.fullmatch(r"[A-Za-z0-9.-]{1,253}", host):
                    raise RuntimeError("节点地址格式：域名或IPv4:FRP端口")
                _port(port)
                ports = str(cfg.get("forward_ports", ""))
                if not re.fullmatch(r"[0-9,-]{1,200}", ports):
                    raise RuntimeError("需要允许的映射端口或范围")
                for interval in ports.split(","):
                    values = interval.split("-")
                    if len(values) > 2 or int(_port(values[0])) > int(_port(values[-1])):
                        raise RuntimeError("端口范围无效")
                wire.update(address=addr, forwardPorts=ports, forward=1, p2p=1, web=2,
                            tunnel=2, proxy=2, protocol="tcp",
                            p2pDisableForward=1 if cfg.get("disable_relay") is True else 2)
        elif action == "enable" and kind in ("forward", "p2p"):
            if type(data.get("enabled")) is not bool:
                raise RuntimeError("enabled 必须为布尔值")
            wire["enable"] = 1 if data["enabled"] else 2
        elif action != "delete":
            raise RuntimeError("不支持的操作")
        if action == "delete" and kind in ("node", "client"):
            if any(row.get(kind + "_id") == key for t in ("forward", "p2p") for row in self.call(t, "list")["data"]):
                raise RuntimeError("仍有隧道引用此对象")
        self._post(kind, action, wire)
        if action == "delete":
            return {"status": "pending_revoke", "data": {"id": key}}
        obj = self._public(kind, self._find(kind, key))
        return {"status": "pending_apply" if kind in ("p2p", "forward") else "saved", "data": obj}


def _port(value):
    if not str(value).isascii() or not str(value).isdigit() or not 1 <= int(value) <= 65535:
        raise RuntimeError("端口必须在 1 至 65535 之间")
    return str(int(value))
