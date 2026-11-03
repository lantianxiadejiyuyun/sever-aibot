import ipaddress
import re
import socket
from urllib.parse import urlsplit
from .store import error


def fields(data, allowed):
    if not isinstance(data,dict) or set(data)-set(allowed.split()):
        error("存在不支持的参数")
    return data


def string(value,limit=160):
    if not isinstance(value,str) or not value.strip() or len(value)>limit or any(ord(c)<32 for c in value):
        error("文本参数无效")
    return value.strip()


def integer(value,lo,hi):
    if type(value) is not int or not lo<=value<=hi:
        error("数值超出范围")
    return value


def hostaddr(value):
    v=string(value,253)
    try:
        ipaddress.ip_address(v)
    except ValueError:
        if not all(re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?",part) for part in v.split(".")):
            error("主机地址无效")
    return v


def url(value,model=False):
    s=string(value,2048)
    try:
        u=urlsplit(s)
        hostaddr(u.hostname)
        _=u.port
    except ValueError:
        error("URL 格式无效")
    if u.username or u.password or u.fragment or u.query or u.scheme not in ("http","https"):
        error("请使用不含凭据、查询串或片段的 HTTP(S) 地址")
    if model and u.scheme!="https" and u.hostname not in ("localhost","127.0.0.1","::1"):
        error("远程模型接口必须使用 HTTPS")
    return s


def purpose(v):
    if v not in ("inspect","deploy"):
        error("无效的账号用途")
    return v


def scope(db,host_id,service_id):
    h=db.get("host",host_id)
    s=db.get("service",service_id) if service_id else None
    if s and s["host_id"]!=h["id"]:
        error("服务不属于所选主机",409)
    return h,s


def separate_probe_host(node,target):
    if node["id"]==target["id"] or node["address"].casefold()==target["address"].casefold():
        return False
    def ips(host):
        rows=socket.getaddrinfo(host,None,type=socket.SOCK_STREAM)
        values=[ipaddress.ip_address(r[4][0].split("%")[0]) for r in rows]
        return {getattr(ip,"ipv4_mapped",None) or ip for ip in values}
    try:
        a,b=ips(node["address"]),ips(target["address"])
        return bool(a and b and a.isdisjoint(b) and not any(ip.is_loopback or ip.is_unspecified or ip.is_multicast for ip in a))
    except (ValueError,OSError):
        return False


def service_config(d,db=None,host_id=None):
    fields(d,"name directory compose_service config_path image_variable health_url ports public_probe")
    if "name" in d:
        d["name"]=string(d["name"])
    for k in ("directory","config_path"):
        if d.get(k):
            p=string(d[k],1024)
            if (k=="directory" and not p.startswith("/")) or ".." in p.split("/") or p.startswith("-"):
                error("无效的部署路径")
    for k,rx in (("compose_service",r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}"),("image_variable",r"[A-Z][A-Z0-9_]{0,79}")):
        if d.get(k) and not re.fullmatch(rx,d[k]):
            error("无效的 Compose 标识")
    if d.get("image_variable") and d["image_variable"] not in ("IMAGE","APP_IMAGE","SERVICE_IMAGE","DOCKER_IMAGE") and not re.fullmatch(r"[A-Z][A-Z0-9_]{0,60}_IMAGE",d["image_variable"]):
        error("镜像变量须为 IMAGE 或以 _IMAGE 结尾，不能覆盖执行环境")
    if d.get("health_url"):
        url(d["health_url"])
    if "ports" in d:
        if not isinstance(d["ports"],list) or len(d["ports"])>64:
            error("端口应为数组")
        for p in d["ports"]:
            integer(p,1,65535)
    if d.get("public_probe"):
        p=d["public_probe"]
        fields(p,"protocol host port path request expect probe_host_id")
        if p.get("probe_host_id"):
            node=db.get("host",string(p["probe_host_id"],80))
            h=db.get("host",host_id)
            if not separate_probe_host(node,h):
                error("外部探测节点必须独立于被管主机")
        hostaddr(p.get("host"));integer(p.get("port"),1,65535)
        if p.get("protocol") not in ("http","https","udp","tcp"):
            error("探测协议无效")
        if not re.fullmatch(r"/[!-~]{0,1023}",p.get("path","/")):
            error("探测路径无效")
        for k in ("request","expect"):
            if k in p and (not isinstance(p[k],str) or len(p[k])>512):
                error("探测报文过大")
    return d


SECRET_FIELD=re.compile(r"secret|token|password|passphrase|private.?key|access.?key|api.?key|authorization|credential|^sk$|^key$|^vkey$|^auth$|visitor.?key",re.I)
def redact(v):
    if isinstance(v,dict):
        return {k:redact(val) for k,val in v.items() if not SECRET_FIELD.search(k)}
    if isinstance(v,list):
        return [redact(x) for x in v]
    if isinstance(v,str):
        v=re.sub(r"-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?(?:-----END [^-]*PRIVATE KEY-----|$)","[私钥已隐藏]",v)
        return re.sub(r"(?i)(?:Bearer\s+\S+|sk-[A-Za-z0-9_-]{8,}|(?:password|token|api[_-]?key|passphrase|access[_-]?key)\s*[:=]\s*[^\s,;]+)","[已隐藏]",v)
    return v
