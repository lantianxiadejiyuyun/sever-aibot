import logging
import re
import threading
import time
from fastapi import HTTPException
from . import inspection, sshops
from .domain import fields, hostaddr, integer, redact, scope, string
from .store import digest, error, now

KINDS=("node","client","forward","p2p")
CHANGES=("create","update","enable","delete")
KEYS={"node":"name address forward_ports disable_relay","client":"name",
      "forward":"name node_id client_id public_port target_host target_port",
      "p2p":"name node_id client_id target_host target_port allow_relay"}


def gateway(kind,action,data):
    try:
        from tunnel.gateway import Gateway
        return redact(Gateway().call(kind,action,data))
    except (ImportError,RuntimeError,OSError) as e:
        # Upstream response bodies can contain secrets; do not expose raw errors.
        error("gostc 管理服务未就绪或拒绝该操作，请核对规则、节点和端口",502)


def remote_id(v):
    if type(v) not in (int,str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,100}",str(v)):
        error("规则或节点编号无效")
    return str(v)


class Jobs:
    def __init__(self,db):
        self.db=db
        self.stop=threading.Event()
        self.wake=threading.Event()
        self.thread=threading.Thread(target=self.loop,name="aibot-executor",daemon=True)

    def start(self):
        for t in self.db.all("task",status="running"):
            self.save(t,status="unknown",result={"note":"进程中断，未重复执行；请核实现状"})
        self.thread.start()

    def close(self):
        self.stop.set();self.wake.set()
        self.thread.join()

    def save(self,t,**attrs):
        return self.db.put("task",{**t,**attrs,"updated_at":now()})

    def bound(self,kind,rid,host_id,service_id=None):
        rec=self.db.get("binding",kind+":"+remote_id(rid))
        if rec["host_id"]!=host_id or (service_id and rec["service_id"]!=service_id) or rec.get("deleted"):
            error("记录不属于当前目标",409)
        return rec

    def listing(self,kind,host_id,service_id=None):
        if kind not in KINDS:
            error("不支持的资源",404)
        scope(self.db,host_id,service_id)
        bindings={r["remote_id"]:r for r in self.db.all("binding",host_id=host_id,kind=kind,
                  service_id=service_id if kind in ("forward","p2p") else None)}
        rows=gateway(kind,"list",{})["data"]
        live=[{**bindings[str(r["id"])],"id":str(r["id"]),"data":r} for r in rows if str(r["id"]) in bindings]
        seen={r["id"] for r in live}
        return live+[{**b,"id":key,"data":b["removed"]} for key,b in bindings.items()
                     if b.get("deleted") and b.get("removed") and key not in seen and b["status"]=="pending_revoke"]

    def rule_guard(self,h,s,kind,action,rid,data):
        if kind not in KINDS or action not in (*CHANGES,"check"):
            error("不支持的规则操作",404)
        hid=h["id"];sid=s["id"] if s else None
        if kind in ("forward","p2p") and not s:
            error("请选择关联服务")
        if kind in ("node","client"):
            sid=None
        old=None
        if action!="create":
            binding=self.bound(kind,rid,hid,sid)
            old=gateway(kind,"check",{"id":binding["upstream_id"]})["data"]
        elif rid:
            error("新规则不能指定编号")
        if action in ("create","update"):
            fields(data,KEYS[kind])
            if not data:
                error("配置不能为空")
            full={**(old or {}),**data}
            string(full.get("name"))
            if kind in ("forward","p2p"):
                for key,typ in (("node_id","node"),("client_id","client")):
                    self.bound(typ,full.get(key),hid)
                hostaddr(full.get("target_host"));integer(full.get("target_port"),1,65535)
                if kind=="forward":
                    integer(full.get("public_port"),1,65535)
                    for other in gateway("forward","list",{})["data"]:
                        if (str(other["id"])!=str(rid) and str(other.get("node_id"))==str(full["node_id"])
                                and other.get("public_port")==full["public_port"]):
                            error("节点 TCP/UDP 公网端口冲突："+str(full["node_id"])+":"+str(full["public_port"]),409)
            elif kind=="node":
                string(full.get("address"));string(full.get("forward_ports"))
            for key in ("allow_relay","disable_relay"):
                if key in data and type(data[key]) is not bool:
                    error("开关须为布尔值")
        elif action=="enable":
            fields(data,"enabled")
            if type(data.get("enabled")) is not bool:
                error("enabled 须为布尔值")
        else:
            fields(data,"")
        return old

    def targetversion(self,h,s):
        jump=self.db.get("host",h["jump_host_id"]) if h.get("jump_host_id") else None
        return digest([h,jump,s])

    def ensure_chat(self,chat_id,hid,sid,write):
        if chat_id:
            c=self.db.get("chat",chat_id)
            if (c.get("host_id")!=hid or (c.get("service_id") or "")!=(sid or "") or
                    (write and c["mode"]!="change")):
                error("会话目标或操作模式不允许该动作",409)
            return c["generation"]
        return 0

    def prepare(self,kind,action,d):
        fields(d,"host_id service_id id data image request_id chat_id trigger")
        h,s=scope(self.db,d.get("host_id"),d.get("service_id"))
        sid=s["id"] if s else None
        write=kind!="inspection" and action!="check"
        gen=self.ensure_chat(d.get("chat_id"),h["id"],sid,write)
        req=d.get("request_id")
        if req:
            string(req,160)
        signature=digest([kind,action,{k:v for k,v in d.items() if k!="request_id"},gen])
        def duplicate():
            if req:
                alias=self.db.get("request",req,False)
                if alias:
                    if alias["signature"]!=signature:
                        error("重复请求编号已用于其他计划",409)
                    return self.db.get("task",alias["task_id"])
                for t in self.db.all("task",request_id=req):
                    if t["signature"]!=signature:
                        error("重复请求编号已用于其他计划",409)
                    return t
        if (existing:=duplicate()):
            return existing
        version=self.targetversion(h,s)
        plan={"host":h["name"],"service":s["name"] if s else None,"actor":"admin","target_version":version}
        if kind=="deploy":
            image=string(d.get("image"),255)
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9./_:@-]{0,254}",image):
                error("镜像引用无效")
            if not s or not all(s.get(k) for k in ("directory","config_path","compose_service","health_url","image_variable")):
                error("需登记 Compose 目录、配置、服务、镜像变量和健康检查")
            if not h.get("bindings",{}).get("deploy"):
                error("请绑定独立部署账号")
            with sshops.connect(self.db,h) as tr:
                before=sshops.live(tr,s)
            plan.update(before=before,after={"image":image},directory=s["directory"],compose_service=s["compose_service"],
                        ports=s["ports"],health_url=s["health_url"],last_verified=s.get("verified"),
                        impact="仅重建选定 Compose 服务，期间可能中断",
                        rollback="可另行预览上一镜像 "+before["image"]+"；未验证该镜像仍可拉取",
                        steps=["核对现场","拉取指定镜像","更新指定服务","核验镜像摘要、端口和健康检查"])
        elif kind!="inspection":
            data=d.get("data") or {}
            before=self.rule_guard(h,s,kind,action,d.get("id"),data)
            plan.update(rule_id=(before or {}).get("id"),before=before,
                        after=None if action=="delete" else {**(before or {}),**data},
                        changes=data,protocols=["TCP","UDP"] if kind=="forward" else ["TCP"] if kind=="p2p" else [],
                        impact={"delete":"移除配置；离线时待撤销","enable":"停止或恢复该入口","create":"创建并下发配置",
                                "update":"替换此配置并下发","check":"只读检查"}.get(action))
        with self.db.atomic():
            if (existing:=duplicate()):
                return existing
            if gen!=self.ensure_chat(d.get("chat_id"),h["id"],sid,write):
                error("目标已变化",409)
            if write:
                for t in self.db.all("task",signature=signature,status="awaiting_confirmation"):
                    if t["plan"]==plan:
                        if req:
                            self.db.put("request",{"id":req,"signature":signature,"task_id":t["id"]})
                        return t
            task=self.db.new("task",host_id=h["id"],service_id=sid,chat_id=d.get("chat_id"),generation=gen,kind=kind,action=action,
                             plan=plan,signature=signature,request_id=req,status="awaiting_confirmation" if write else "queued",
                             steps=[],result=None,updated_at=now(),trigger=d.get("trigger","manual"),confirmed_at=None)
        self.wake.set()
        return task

    def guard(self,t):
        h,s=scope(self.db,t["host_id"],t["service_id"])
        if self.targetversion(h,s)!=t["plan"]["target_version"] or self.ensure_chat(t["chat_id"],h["id"],t["service_id"],True)!=t["generation"]:
            error("目标已变化，需重新预览",409)
        if t["kind"] in KINDS:
            p=t["plan"]
            observed=self.rule_guard(h,s,t["kind"],t["action"],p["rule_id"],p["changes"])
            if observed and observed.get("version")!=p["before"].get("version"):
                error("上游配置版本已变化，需重新预览",409)

    def confirm(self,key):
        t=self.db.get("task",key)
        if t["status"]=="stale":
            error("该计划已失效",409)
        if t["status"]!="awaiting_confirmation":
            return t
        try:
            self.guard(t)
        except HTTPException as e:
            if e.status_code in (403,404,409):
                with self.db.atomic():
                    current=self.db.get("task",key)
                    if current["status"]=="awaiting_confirmation":
                        self.save(current,status="stale",result={"detail":e.detail})
            raise
        with self.db.atomic():
            t=self.db.get("task",key)
            if t["status"]!="awaiting_confirmation":
                return t
            t=self.save(t,status="queued",confirmed_at=now(),confirmed_by="admin")
        self.wake.set()
        return t

    def evidence(self,s):
        items=[]
        for b in self.db.all("binding",service_id=s["id"]):
            if b.get("deleted") or b["kind"] not in ("forward","p2p"):
                continue
            try:
                res=gateway(b["kind"],"check",{"id":b["upstream_id"]})
                e=res["data"]
                items.append(inspection.item("gostc:"+b["id"],"unverified",{"configuration":e,"status":res.get("status"),
                          "business":"配置和在线状态不能证明业务可用"},"gostc"))
            except HTTPException:
                items.append(inspection.item("gostc:"+b["id"],"unverified","无法读取管理端证据","gostc"))
        return items

    def execute(self,t):
        h,s=scope(self.db,t["host_id"],t["service_id"])
        k,a,p=t["kind"],t["action"],t["plan"]
        def step(text):
            t["steps"].append({"time":now(),"text":text})
            self.save(t)
        if k=="inspection":
            r=inspection.inspect(self.db,h,s,t["trigger"],self.evidence)
            return {"report_id":r["id"]}
        if a!="check":
            if not t["confirmed_at"]:
                error("缺少确认",403)
            self.guard(t)
        if k=="deploy":
            s["state"]="待核实";s["expected"]={"image":p["after"]["image"],"task_id":t["id"]}
            self.db.put("service",s)
            try:
                with sshops.connect(self.db,h,"deploy") as tr:
                    result=sshops.deploy(tr,s,p["after"]["image"],p["before"],step)
                fresh=self.db.get("service",s["id"])
                if fresh!=s:
                    error("执行期间档案已更改；需重新核实",409)
                s.update(verified={**result,"verified_at":now(),"task_id":t["id"],"rollback_image":p["before"]["image"],
                                   "directory":s["directory"],"config_path":s["config_path"],"health_url":s["health_url"]},state="verified")
                self.db.put("service",s)
            finally:
                report=inspection.inspect(self.db,h,s,"deployment",self.evidence)
                t["report_id"]=report["id"]
            return {"verified":s["verified"],"report_id":report["id"]}
        args={**p["changes"]}
        if p["rule_id"] is not None:
            args["id"]=p["rule_id"]
            if a!="check":
                args["version"]=p["before"]["version"]
        step("已核对目标；调用 gostc "+a)
        result=gateway(k,a,args)
        data=result["data"]
        rid=p["rule_id"] if a!="create" else data["id"]
        key=k+":"+remote_id(rid)
        b=self.db.get("binding",key,False) or {"id":key,"kind":k,"remote_id":str(rid),"upstream_id":rid,
              "host_id":h["id"],"service_id":s["id"] if s and k in ("forward","p2p") else None}
        if a!="check":
            b.update(status=result["status"],deleted=a=="delete",expected=p["after"],task_id=t["id"],updated_at=now())
            if a=="delete":
                b["removed"]=p["before"]
            self.db.put("binding",b)
        if k in ("forward","p2p") and s:
            cfg=data if a!="delete" else p["before"]
            if a=="delete" or (a=="enable" and not args["enabled"]):
                status,ev=inspection.mapping_probe(s,k,cfg)
                result["verification"]=inspection.item("public","abnormal" if status=="ok" else "unverified",
                     "停用后仍有业务响应" if status=="ok" else "撤销尚未验证："+ev,"已登记公网入口")
            else:
                status,ev=inspection.mapping_probe(s,k,cfg)
                result["verification"]=inspection.item("public",status,ev,"已登记规则入口；仅对应所测协议")
                if "target_host" in cfg and "target_port" in cfg:
                    result["target"]=inspection.rule_target(self.db,h,s,cfg)
            b["last_check"]=result["verification"]
            if a=="check":
                r=inspection.inspect(self.db,h,s,"tunnel",self.evidence)
                result["report_id"]=r["id"]
        self.db.put("binding",b)
        return result

    def loop(self):
        while not self.stop.is_set():
            try:
                self.schedule()
                queued=self.db.all("task",status="queued")
                if not queued:
                    self.wake.wait(1);self.wake.clear();continue
                with self.db.atomic():
                    t=self.db.get("task",queued[-1]["id"])
                    if t["status"]!="queued":
                        continue
                    t=self.save(t,status="running")
                try:
                    result=self.execute(t)
                    self.save(t,status="succeeded",result=result)
                except Exception as exc:
                    status="stale" if isinstance(exc,HTTPException) and exc.status_code==409 and not t["steps"] else "failed"
                    if t["steps"] and (t["kind"]=="deploy" or (t["action"]!="check" and getattr(exc,"status_code",0)==502)):
                        status="unknown"
                    self.save(t,status=status,result={"detail":getattr(exc,"detail","执行未完成，请核实连接或目标配置")})
            except Exception as exc:
                logging.error("Worker iteration failed: %s",type(exc).__name__)
                self.stop.wait(1)

    def schedule(self):
        for s in self.db.all("schedule"):
            if s["interval_minutes"] and s["next_run"]<=time.time():
                s["next_run"]=time.time()+s["interval_minutes"]*60
                self.db.put("schedule",s)
                self.prepare("inspection","inspect",{"host_id":s["id"],"trigger":"scheduled"})
