import http.client
import ipaddress
import os
import re
import json
import shlex
import socket
import ssl
from . import sshops, push
from .store import now, digest

# aaPanel firewallModel/comModel.py: set_forward_db stores these four columns in
# firewall_forward. Different schemas/locations are unsupported, never inferred from NAT.
BT_VERSION = r"""import re,sys
from pathlib import Path
root=Path(sys.argv[1]);denied=False
for file,pattern in [('class/common.py',r'''(?m)^\s*g\.version\s*=\s*["']([0-9]+(?:\.[0-9]+){1,3})["']'''),
                     ('data/version.pl',r'^\s*([0-9]+(?:\.[0-9]+){1,3})\s*$')]:
 try:
  with (root/file).open(errors='replace') as f:match=re.search(pattern,f.read(1048576))
  if match:print(match.group(1));sys.exit(0)
 except PermissionError:denied=True
 except OSError:pass
sys.exit(77 if denied else 1)"""

BT_QUERY = """import sqlite3,json,sys
from pathlib import Path
root=Path(sys.argv[1])
found={};denied=False
for path in ('data/db/firewall.db','data/default.db'):
 try:
  with (root/path).open('rb'):pass
  con=sqlite3.connect((root/path).as_uri()+'?mode=ro',uri=True,timeout=1)
 except PermissionError:denied|=path=='data/db/firewall.db';continue
 except sqlite3.Error:denied|=path=='data/db/firewall.db';continue
 except OSError:continue
 try:
  for table,cols in [('firewall_forward','Protocol,S_Port,T_Address,T_Port'),('firewall_trans','protocol,start_port,ended_ip,ended_port')]:
   try:found[path+':'+table]=con.execute('SELECT '+cols+' FROM '+table+' LIMIT 256').fetchall()
   except sqlite3.Error as e:
    code=getattr(e,'sqlite_errorcode',0)&255
    if code in (3,8,14,23):denied|=path=='data/db/firewall.db'
 finally:con.close()
if not found:sys.exit(77 if denied else 1)
print(json.dumps(found))
sys.exit(77 if denied else 0)"""

# Extract route names and upstream authorities only. Never return config bodies,
# headers, query strings or URI userinfo to the control server.
PROXY_SUMMARY = r"""import glob,re,os,urllib.parse,json
rows=[]
paths=glob.glob('/www/server/panel/vhost/nginx/proxy/*/*.conf')+glob.glob('/www/server/panel/vhost/nginx/*.conf')
if not paths: raise SystemExit(1)
read=0
for path in paths[:128]:
 try:
  with open(path,errors='replace') as f:body=f.read(65536)
 except OSError:continue
 read+=1
 for dest in re.findall(r'^\s*proxy_pass\s+(https?://[^;\s]+);',body,re.M):
  try:u=urllib.parse.urlsplit(dest);host=u.hostname;port=u.port
  except ValueError:continue
  if host and re.fullmatch('[a-zA-Z0-9.:-]+',host):
   rows.append([os.path.basename(path),u.scheme,host,port])
if not read:raise SystemExit(1)
print(json.dumps(rows[:128]))"""

# Read only selected fields: no environment, command lines, raw logs or config dumps.
COMMANDS = {
    "uptime": "cut -d' ' -f1 /proc/uptime",
    "load": "cut -d' ' -f1-3 /proc/loadavg",
    "cpu": """awk '/^cpu /{print $2+$3+$4+$5+$6+$7+$8+$9,$5+$6; exit}' /proc/stat; sleep 1; awk '/^cpu /{print $2+$3+$4+$5+$6+$7+$8+$9,$5+$6; exit}' /proc/stat""",
    "memory": """awk '/^(MemTotal|MemAvailable):/{print $1,$2,$3}' /proc/meminfo""",
    "disk": "df -P / | tail -1",
    "network": """awk -F'[: ]+' 'NR>2 {print $2,$3,$11}' /proc/net/dev""",
    "systemd": "systemctl list-units --type=service --state=running --no-legend --no-pager | awk '{print $1}'",
    "docker": """docker ps --format '{{.Names}} {{.Status}} {{.Ports}}'""",
    "listeners": "ss -H -lntu | awk '{print $1,$5}'",
    "bt_version": "python3 -c "+shlex.quote(BT_VERSION)+" /www/server/panel",
    "web_proxy": "python3 -c "+shlex.quote(PROXY_SUMMARY),
    "nat": "iptables -w 2 -t nat -S",
    "firewall": "iptables -w 2 -S",
}


def public_probe(spec):
    if not spec or os.getenv("AIBOT_EXTERNAL_PROBE") != "1":
        return "unverified","公网未验证：未登记入口或未配置外部探测位置"
    host, port, proto = spec["host"], spec["port"], spec["protocol"]
    try:
        typ = socket.SOCK_DGRAM if proto == "udp" else socket.SOCK_STREAM
        entries = socket.getaddrinfo(host, port, type=typ)
        if not entries or any(not ipaddress.ip_address(x[4][0]).is_global or ipaddress.ip_address(x[4][0]).is_multicast for x in entries):
            return "unverified","外部探测只允许登记的公网地址"
        af,typ,p,_,addr = entries[0]
        with socket.socket(af,typ,p) as conn:
            conn.settimeout(5)
            conn.connect(addr)
            if proto in ("https","http"):
                sock = ssl.create_default_context().wrap_socket(conn,server_hostname=host) if proto=="https" else conn
                try:
                    authority = ("["+host+"]" if ":" in host else host)+":"+str(port)
                    sock.sendall(("GET "+spec.get("path","/")+" HTTP/1.1\r\nHost: "+authority+"\r\nConnection: close\r\n\r\n").encode("ascii"))
                    res = http.client.HTTPResponse(sock)
                    res.begin()
                    body = res.read(65536)
                    if 200 <= res.status < 300 and not spec.get("expect"):
                        return "unverified","HTTP "+str(res.status)+"；未登记预期业务响应特征"
                    ok = 200 <= res.status < 300 and spec.get("expect","").encode() in body
                    return ("ok" if ok else "abnormal"), "业务响应 "+str(res.status)+(" 匹配" if ok else " 不匹配")
                finally:
                    if sock is not conn:
                        sock.close()
            if not spec.get("expect") or not spec.get("request"):
                return "unverified", "TCP 握手成功；未验证业务响应" if proto=="tcp" else "UDP 未登记请求及预期响应"
            conn.sendall(spec["request"].encode())
            expect = spec["expect"].encode()
            buf = conn.recv(65536)
            while proto=="tcp" and buf and expect not in buf and len(buf)<65536:
                chunk=conn.recv(4096)
                if not chunk:
                    break
                buf+=chunk
            ok = expect in buf
            return ("ok" if ok else "abnormal"), proto.upper()+(" 业务响应匹配" if ok else " 业务响应不匹配")
    except (OSError,ValueError,http.client.HTTPException):
        return ("unverified" if proto=="udp" else "abnormal"), proto.upper()+" 未收到预期响应"


def item(name, status, evidence, source):
    return dict(name=name,status=status,evidence=evidence,source=source,time=now())


TARGET_PROBE = """import http.client,json,socket,sys,ssl
p=json.loads(sys.argv[1])
proto=p.get('protocol','tcp')
host=p['host'];port=p['port'];expect=p.get('expect','').encode()
try:
 if proto in ('http','https'):
  cls=http.client.HTTPSConnection if proto=='https' else http.client.HTTPConnection
  con=cls(host,port,timeout=4)
  con.request('GET',p.get('path','/'))
  r=con.getresponse();body=r.read(65536)
  status='abnormal' if not 200<=r.status<300 or (expect and expect not in body) else ('ok' if expect else 'unverified')
  print(json.dumps([status,'HTTP '+str(r.status)+('; response matched' if status=='ok' else '')]))
 else:
  typ=socket.SOCK_DGRAM if proto=='udp' else socket.SOCK_STREAM
  af,t,pr,_,addr=socket.getaddrinfo(host,port,type=typ)[0]
  with socket.socket(af,t,pr) as conn:
   conn.settimeout(4);conn.connect(addr)
   if expect and p.get('request'):
    conn.sendall(p['request'].encode());buf=conn.recv(65536)
    print(json.dumps(['ok','response matched'] if expect in buf else ['abnormal','unexpected response']))
   else: print(json.dumps(['unverified','TCP handshake only' if proto=='tcp' else 'UDP payload not registered']))
except (OSError,ValueError,http.client.HTTPException):
 print(json.dumps(['unverified' if proto=='udp' else 'abnormal','no expected response']))
"""


def rule_target(db,host,s,config):
    """From the bound host, probe only the target already in the upstream rule."""
    probe=dict(s.get("public_probe") or {})
    probe.update(host=config["target_host"],port=config["target_port"])
    if config.get("public_port") is None:  # P2P is always TCP
        if probe.get("protocol")=="udp":
            probe={"protocol":"tcp","host":config["target_host"],"port":config["target_port"]}
    try:
        with sshops.connect(db,host) as tr:
            rc,out=sshops.run(tr,"python3 -c "+shlex.quote(TARGET_PROBE)+" "+shlex.quote(json.dumps(probe)),timeout=8)
        status,ev=json.loads(out) if not rc else ("unverified","远程探测工具不可用")
    except Exception:
        status,ev="unverified","未取得被管主机到规则目标的响应证据"
    return item("target",status,ev,"已绑定主机 SSH")


def mapping_probe(service,kind,config):
    p=service.get("public_probe")
    # A different service endpoint, or a P2P visitor, does not verify this rule.
    host=(config.get("public_host") or "").rpartition(":")[0].lower()
    if kind!="forward" or not p or p["port"]!=config.get("public_port") or p["host"].lower()!=host:
        return "unverified","未登记与此规则公网节点地址和端口匹配的外部探测"
    return public_probe(p)


def inspect(db, host, service=None, trigger="manual", gateway_evidence=None):
    host_id=host["id"]
    service_id=service["id"] if service else ""
    items=[]
    metrics={}
    version_denied=False
    targets=[service] if service else db.all("service",host_id=host_id)
    try:
        with sshops.connect(db,host) as tr:
            items.append(item("ssh","ok","已核对主机指纹并完成受限账号认证","SSH"))
            for name, cmd in COMMANDS.items() if trigger!="deployment" else []:
                rc, val = sshops.run(tr,"bash -o pipefail -c "+sshops.Q(cmd),limit=8192)
                if name=="bt_version":
                    version_denied=rc==77
                if name in ("nat","firewall"):
                    # Drop comments / arbitrary data; keep only protocol, chain and destination tuples.
                    val="\n".join(" ".join(re.findall(r"(?:^-[AN] [A-Za-z0-9_-]+|(?:-p|--dport|-j|--to-destination) [\w.:-]+)",line)) for line in val.splitlines() if line.startswith("-A "))[:8192]
                if name=="cpu" and not rc:
                    try:
                        (t1,i1),(t2,i2)=[tuple(map(int,l.split())) for l in val.splitlines()]
                        val=round(100*(1-(i2-i1)/(t2-t1)),2)
                    except (ValueError,ZeroDivisionError):
                        rc=1
                if not rc:
                    metrics[name]=val
                items.append(item(name,"ok" if not rc else "unverified",val if not rc else "命令不可用或无读取权限","SSH"))
            if trigger!="deployment":
                panel_rules=None
                source="SSH"
                rc,raw=sshops.run(tr,"python3 -c "+sshops.Q(BT_QUERY)+" /www/server/panel",limit=98304)
                if rc in (0,77) and raw:
                    try:
                        panel_rules=json.loads(raw)
                    except ValueError:
                        pass
                if rc==77 or version_denied:
                    helper_rc,raw=sshops.run(tr,"/usr/bin/sudo -n -- /usr/local/libexec/aibot-bt-evidence",limit=98304)
                    if not helper_rc:
                        try:
                            evidence=json.loads(raw)
                        except ValueError:
                            evidence=None
                        if isinstance(evidence,dict):
                            version=evidence.get("version")
                            if not metrics.get("bt_version") and isinstance(version,str) and re.fullmatch(r"[0-9]+(?:\.[0-9]+){1,3}",version):
                                metrics["bt_version"]=version
                                items=[i for i in items if i["name"]!="bt_version"]
                                items.append(item("bt_version","ok",version,"宝塔只读助手"))
                            if isinstance(evidence.get("rules"),dict) and evidence["rules"]:
                                panel_rules={**(panel_rules or {}),**evidence["rules"]}
                                source="宝塔只读助手"
                if isinstance(panel_rules,dict) and panel_rules:
                    items.append(item("bt_rules","ok",panel_rules,source+"；宝塔 "+metrics.get("bt_version","版本未核实")+"；可读配置表（最多各 256 条）"))
                    unique={tuple(map(str,row)) for table in panel_rules.values() for row in table if len(row)==4}
                    for proto,port,ip,dest in sorted(unique):
                        if not (re.fullmatch(r"(?i)tcp|udp",str(proto)) and str(port).isdigit() and str(dest).isdigit() and re.fullmatch(r"[0-9.]{7,15}",str(ip))):
                            continue
                        match=any(re.search(r"(?:^| )-p "+proto.lower()+r"(?: |$)",line)
                                  and re.search(r"--dport "+str(port)+r"(?: |$)",line)
                                  and re.search(r"--to-destination "+re.escape(ip+":"+str(dest))+r"(?: |$)",line)
                                  for line in metrics.get("nat","").splitlines())
                        items.append(item("nat:"+proto+":"+str(port),"ok" if match else "unverified",
                                          "匹配内核 DNAT 规则" if match else "未核实匹配的 DNAT 规则","iptables"))
                        for selected in targets:
                            p=selected.get("public_probe") or {}
                            if p.get("port")!=int(port) or ("udp" if p.get("protocol")=="udp" else "tcp")!=proto.lower():
                                continue
                            args={**p,"host":ip,"port":int(dest)}
                            rc,out=sshops.run(tr,"python3 -c "+shlex.quote(TARGET_PROBE)+" "+shlex.quote(json.dumps(args)),timeout=8)
                            try:
                                st,ev=json.loads(out) if not rc else ("unverified","远程探测工具不可用")
                            except ValueError:
                                st,ev="unverified","无有效探测结果"
                            items.append(item("bt_target:"+selected["id"],st,ev,"宝塔转发机到 "+ip+":"+str(dest)))
            for s in targets:
                status,ev = sshops.health(tr,s.get("health_url"))
                items.append(item("health:"+s["id"],status,ev,"被管主机"))
    except Exception as exc:
        items=[i for i in items if i["name"]!="ssh"]
        items.append(item("ssh","abnormal",getattr(exc,"detail","SSH 连接或采集失败"),"SSH"))
    if trigger!="deployment" and not any(i["name"]=="bt_rules" for i in items):
        items.append(item("bt_rules","unverified","宝塔配置未核实；系统规则不能证明来源","宝塔"))
    for s in targets:
        if not any(i["name"]=="health:"+s["id"] for i in items):
            items.append(item("health:"+s["id"],"unverified","没有本次目标响应证据","被管主机"))
        status, ev = public_probe(s.get("public_probe"))
        items.append(item("public:"+s["id"],status,ev,os.getenv("AIBOT_PROBE_NAME","控制端外部探测")))
        if gateway_evidence:
            items.extend(gateway_evidence(s))
    previous=next((r for r in db.all("report",host_id=host_id, service_id=service_id)
                   if (r["trigger"]=="deployment")==(trigger=="deployment")),None)
    old={i["name"]:i["status"] for i in (previous or {}).get("items",[])}
    changes=[{"name":i["name"],"before":old.get(i["name"]),"after":i["status"]} for i in items if old.get(i["name"])!=i["status"]]
    with db.atomic():
        report=db.new("report",host_id=host_id,service_id=service_id,trigger=trigger,items=items,changes=changes,
                      scope=[s["id"] for s in targets],suggestion="复查异常及未验证项的证据来源")
        if trigger!="deployment":
            stale=any(i["name"]=="ssh" and i["status"]!="ok" for i in items)
            last=db.all("monitor",host_id=host_id)[:1]
            db.new("monitor",host_id=host_id,metrics=(last[0]["metrics"] if stale and last else metrics),
                   collected_at=(last[0]["collected_at"] if stale and last else (None if stale else now())),stale=stale,report_id=report["id"])
        for i in items:
            if i["status"] not in ("ok","abnormal"):
                continue
            alarm=digest([host_id,i["name"]])
            prior=(db.get("alarm",alarm,False) or {}).get("status")
            if prior==i["status"]:
                continue
            db.put("alarm",{"id":alarm,"status":i["status"]})
            if i["status"]=="abnormal" or prior=="abnormal":
                sid=i["name"].partition(":")[2] if i["name"].startswith(("health:","public:","bt_target:")) else service_id
                n=db.new("notification",host_id=host_id,service_id=sid,report_id=report["id"],name=i["name"],before=prior,after=i["status"])
                push.enqueue(db,n)
    return report
