import hashlib
import hmac
import os
import re
import secrets
import time
from contextlib import asynccontextmanager
from urllib.parse import urlsplit

from fastapi import FastAPI, APIRouter, Body, Request, Response, HTTPException
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from . import sshops, push
from .controller import Controller, completion, context
from .domain import fields, hostaddr, integer, purpose, redact, scope, service_config, string, url
from .jobs import Jobs
from .store import Store, error, now

COOKIE="aibot_session"


@asynccontextmanager
async def lifespan(app):
    global db,jobs,ai
    db=Store();jobs=Jobs(db);ai=Controller(db,jobs)
    jobs.start()
    sender=push.Sender(db)
    sender.start()
    try:
        yield
    finally:
        jobs.close();sender.close();db.conn.close()


app=FastAPI(lifespan=lifespan,docs_url=None,redoc_url=None,openapi_url=None)
api=APIRouter(prefix="/api")


class LimitBody:
    def __init__(self,app):
        self.app=app

    async def __call__(self,scope,receive,send):
        if scope["type"]!="http" or scope["method"] in ("GET","HEAD","OPTIONS"):
            return await self.app(scope,receive,send)
        buf=bytearray()
        while True:
            msg=await receive()
            if msg["type"]=="http.disconnect":
                return
            buf.extend(msg.get("body",b""))
            if len(buf)>131072:
                return await JSONResponse({"detail":"请求体过大"},413)(scope,receive,send)
            if not msg.get("more_body"):
                break
        sent=False
        async def buffered():
            nonlocal sent
            if sent:
                return await receive()
            sent=True
            return {"type":"http.request","body":bytes(buf),"more_body":False}
        return await self.app(scope,buffered,send)


def session_id(r):
    return hashlib.sha256(r.cookies.get(COOKIE,"").encode()).hexdigest()


def active(r):
    s=db.get("session",session_id(r),False)
    return s if s and s["expires"]>time.time() else None


@app.middleware("http")
async def auth(r:Request,call_next):
    path=r.url.path
    if path.startswith("/api/"):
        write=r.method not in ("GET","HEAD","OPTIONS")
        if write:
            origin=r.headers.get("origin")
            if (origin and urlsplit(origin).netloc!=r.headers.get("host")) or r.headers.get("sec-fetch-site")=="cross-site":
                return JSONResponse({"detail":"跨站请求已拒绝"},403)
            if r.method!="DELETE" and r.headers.get("content-type","").split(";")[0]!="application/json":
                return JSONResponse({"detail":"请提交 JSON"},415)
        if path not in ("/api/session","/api/login"):
            s=active(r)
            if not s:
                return JSONResponse({"detail":"请登录"},401)
            if write and not hmac.compare_digest(s["csrf"].encode(),r.headers.get("x-csrf-token","").encode()):
                return JSONResponse({"detail":"CSRF 校验失败"},403)
    res=await call_next(r)
    res.headers["Cache-Control"]="no-store"
    res.headers["X-Content-Type-Options"]="nosniff"
    return res


app.add_middleware(LimitBody)


@app.exception_handler(RequestValidationError)
async def validation(r,exc):
    # Default errors repeat submitted input, which could be a private key.
    return JSONResponse({"detail":"请求格式错误"},422)


@app.exception_handler(Exception)
async def unavailable(r,exc):
    return JSONResponse({"detail":"执行失败，请核实连接或目标配置"},502)


@api.get("/session")
def session(r:Request):
    s=active(r)
    return {"authenticated":bool(s),"csrf":s["csrf"] if s else None}


@api.post("/login")
def login(r:Request,res:Response,d:dict):
    v=d.get("password")
    if not isinstance(v,str) or not 0<len(v)<=1024:
        error("密码不正确",401)
    ip=r.client.host if r.client else "local"
    with db.atomic():
        rec=db.get("attempt",ip,False) or {}
        if rec.get("since",0)<time.time()-300:
            rec={"id":ip,"count":0,"since":time.time()}
        if rec["count"]>=10:
            error("尝试过于频繁，请稍后再试",429)
        valid=hmac.compare_digest(db.hashpass(v),db.password)
        if not valid:
            rec["count"]+=1;db.put("attempt",rec)
        else:
            db.delete("attempt",ip)
            db.delete("session",session_id(r))
            for old in db.all("session"):
                if old["expires"]<=time.time():
                    db.delete("session",old["id"])
            token=secrets.token_urlsafe(40);csrf=secrets.token_urlsafe(32)
            db.put("session",{"id":hashlib.sha256(token.encode()).hexdigest(),"csrf":csrf,"expires":time.time()+28800})
    if not valid:
        error("密码不正确",401)
    res.set_cookie(COOKIE,token,max_age=28800,httponly=True,secure=os.getenv("AIBOT_COOKIE_SECURE","1")!="0",
                   samesite="strict",path="/api")
    return {"authenticated":True,"csrf":csrf}


@api.post("/logout")
def logout(r:Request,res:Response):
    db.delete("session",session_id(r));res.delete_cookie(COOKIE,path="/api")
    return {"authenticated":False,"csrf":None}


def host_config(d,old=None):
    fields(d,"name address port environment jump_host_id")
    h={**(old or {"port":22,"environment":"default","fingerprint":None,"jump_host_id":None,"bindings":{}}),**d}
    h["name"]=string(h.get("name"));h["address"]=hostaddr(h.get("address"));integer(h["port"],1,65535);string(h["environment"])
    if h.get("jump_host_id"):
        j=db.get("host",h["jump_host_id"])
        if j.get("jump_host_id") or h.get("id")==j["id"] or (old and db.all("host",jump_host_id=old["id"])):
            error("跳板配置不能循环或超过一层")
    if old and any(h.get(k)!=old.get(k) for k in ("address","port","jump_host_id")):
        h.update(fingerprint=None,bindings={})
    return h


@api.get("/hosts")
def hosts():
    return db.all("host")


@api.post("/hosts")
def add_host(d:dict):
    return db.new("host",**host_config(d))


@api.patch("/hosts/{key}")
def edit_host(key:str,d:dict):
    with db.atomic():
        return db.put("host",host_config(d,db.get("host",key)))


@api.post("/hosts/{key}/fingerprint")
def fingerprint(key:str):
    h=db.get("host",key)
    with sshops.connect(db,h,discover=True) as f:
        return {"fingerprint":f,"trusted":f==h.get("fingerprint")}


@api.post("/hosts/{key}/trust")
def trust(key:str,d:dict):
    h=db.get("host",key)
    observed=fingerprint(key)["fingerprint"]
    if observed!=d.get("fingerprint"):
        error("指纹与现场不符；未更改信任",409)
    with db.atomic():
        if h!=db.get("host",key):
            error("目标已变化",409)
        return db.put("host",{**h,"fingerprint":observed})


@api.get("/credentials")
def credentials():
    return db.all("credential")


@api.post("/credentials")
def import_credential(d:dict):
    fields(d,"name purpose private_key passphrase")
    k=sshops.read_key(d.get("private_key"),d.get("passphrase"))
    with db.atomic():
        c=db.new("credential",name=string(d.get("name")),purpose=purpose(d.get("purpose")),fingerprint=sshops.fp(k),enabled=True)
        db.secret("ssh:"+c["id"],{"private_key":d["private_key"],"passphrase":d.get("passphrase")})
    return c


@api.post("/credentials/{key}/disable")
def disable(key:str):
    return db.put("credential",{**db.get("credential",key),"enabled":False})


@api.post("/hosts/{key}/bind")
def bind(key:str,d:dict):
    p=purpose(d.get("purpose"));username=string(d.get("username"),80)
    if not re.fullmatch(r"[a-z_][a-z0-9_-]*\$?",username) or username=="root":
        error("请使用非 root 专用账号")
    c=db.get("credential",d.get("credential_id"))
    if not c["enabled"] or c["purpose"]!=p:
        error("密钥已停用或用途不符")
    with db.atomic():
        h=db.get("host",key);b=h["bindings"]
        for other,rec in b.items():
            if other!=p and (rec["username"]==username or db.get("credential",rec["credential_id"])["fingerprint"]==c["fingerprint"]):
                error("巡检和部署必须使用不同账号及密钥")
        b[p]={"username":username,"credential_id":c["id"]}
        return db.put("host",h)


@api.post("/hosts/{key}/test")
def test_host(key:str,d:dict=Body(default={})):
    with sshops.connect(db,db.get("host",key),purpose(d.get("purpose","inspect"))):
        return {"connected":True,"time":now()}


@api.get("/services")
def services(host_id:str|None=None):
    return db.all("service",host_id=host_id or None)


@api.post("/services")
def add_service(d:dict):
    hid=d.pop("host_id",None);db.get("host",hid)
    cfg=service_config({"name":"","ports":[],"directory":"","config_path":"compose.yaml","compose_service":"",
                        "image_variable":"IMAGE","health_url":"","public_probe":None,**d},db,hid)
    return db.new("service",host_id=hid,**cfg,state="unverified",expected=None,verified=None)


@api.patch("/services/{key}")
def edit_service(key:str,d:dict):
    with db.atomic():
        s=db.get("service",key)
        if db.all("task",service_id=key,status="running"):
            error("任务执行中，请稍后修改服务档案",409)
        return db.put("service",{**s,**service_config(d,db,s["host_id"]),"state":"待核实"})


@api.get("/monitor")
def monitor(host_id:str):
    db.get("host",host_id)
    hist=db.all("monitor",host_id=host_id)[:90]
    return {"latest":hist[0] if hist else None,"history":hist,"stale":hist[0]["stale"] if hist else True}


@api.post("/inspections")
def inspections(d:dict):
    fields(d,"host_id service_id request_id chat_id")
    return jobs.prepare("inspection","inspect",d)


@api.get("/reports")
def reports(host_id:str|None=None,service_id:str|None=None):
    return [r for r in db.all("report",host_id=host_id or None) if not service_id or
            r["service_id"]==service_id or service_id in r["scope"]]


@api.get("/reports/{key}")
def report(key:str):
    return db.get("report",key)


@api.get("/tasks")
def tasks(host_id:str|None=None,service_id:str|None=None):
    return db.all("task",host_id=host_id or None,service_id=service_id or None)


@api.get("/tasks/{key}")
def task(key:str):
    return db.get("task",key)


@api.post("/tasks/{key}/confirm")
def confirm(key:str):
    return jobs.confirm(key)


@api.post("/deployments")
def deployments(d:dict):
    fields(d,"host_id service_id chat_id image request_id")
    return jobs.prepare("deploy","deploy",d)


@api.get("/tunnels/{kind}")
def tunnels(kind:str,host_id:str,service_id:str|None=None):
    return jobs.listing(kind,host_id,service_id or None)


@api.post("/tunnels/{kind}/{action}")
def tunnel_change(kind:str,action:str,d:dict):
    from .jobs import KINDS
    if kind not in KINDS:
        error("不支持的规则类型",404)
    fields(d,"host_id service_id id data request_id chat_id")
    return jobs.prepare(kind,action,d)


@api.get("/hosts/{key}/schedule")
def get_schedule(key:str):
    db.get("host",key)
    return db.get("schedule",key,False) or {"id":key,"interval_minutes":0}


@api.put("/hosts/{key}/schedule")
def schedule(key:str,d:dict):
    db.get("host",key)
    mins=integer(d.get("interval_minutes"),0,43200)
    return db.put("schedule",{"id":key,"interval_minutes":mins,"next_run":time.time()+mins*60})


@api.get("/notifications")
def notifications():
    return db.all("notification")[:200]


@api.get("/push/key")
def push_key():
    return push.key(db)


@api.post("/push/subscriptions")
def subscribe(d:dict):
    return push.subscribe(db,d)


@api.delete("/push/subscriptions")
def unsubscribe(d:dict):
    return push.unsubscribe(db,d.get("endpoint"))


def model_config(d,old=None):
    fields(d,"name base_url model api_key capacity tools is_default")
    m={**(old or {"capacity":16384,"tools":False,"is_default":False}),**d}
    secret=m.pop("api_key",None)
    for key in ("name","model"):
        m[key]=string(m.get(key))
    m["base_url"]=url(m.get("base_url"),True).rstrip("/")
    integer(m["capacity"],4096,2097152)
    if not all(type(m[k]) is bool for k in ("tools","is_default")):
        error("开关须为布尔值")
    if secret is not None and (not isinstance(secret,str) or len(secret)>4096 or any(ord(ch)<32 for ch in secret)):
        error("密钥格式无效")
    if old and old["base_url"]!=m["base_url"] and secret is None and old["has_key"]:
        error("更换接口地址须重新输入密钥，或显式清空")
    with db.atomic():
        if m["is_default"]:
            for x in db.all("model",is_default=True):
                db.put("model",{**x,"is_default":False})
        m["has_key"]=bool(secret) if secret is not None else bool(old and old["has_key"])
        rec=db.put("model",m) if old else db.new("model",**m)
        if secret is not None or not old:
            db.secret("model:"+rec["id"],secret or "")
    return rec


@api.get("/models")
def models():
    return db.all("model")


@api.post("/models")
def add_model(d:dict):
    return model_config(d)


@api.patch("/models/{key}")
def edit_model(key:str,d:dict):
    return model_config(d,db.get("model",key))


@api.post("/models/{key}/test")
def model_test(key:str):
    completion(db,db.get("model",key),[{"role":"user","content":"Reply OK."}],max_tokens=32)
    return {"connected":True,"time":now()}


def chat_config(d,old=None):
    fields(d,"title host_id service_id model_id mode exclude_sources constraints")
    c={**(old or {"host_id":None,"service_id":None,"model_id":None,"title":"新会话","mode":"inspect","exclude_sources":[],"generation":0}),**d}
    c["title"]=string(c["title"])
    c["constraints"]=redact(string(c["constraints"],512)) if c.get("constraints") else ""
    if c["mode"] not in ("inspect","change") or not isinstance(c["exclude_sources"],list) or any(x not in ("history","report") for x in c["exclude_sources"]):
        error("会话设置无效")
    if c.get("host_id") or c.get("service_id"):
        scope(db,c["host_id"],c["service_id"])
    if c.get("model_id"):
        db.get("model",c["model_id"])
    with db.atomic():
        target_changed=old and any(c[k]!=old[k] for k in ("host_id","service_id"))
        c.setdefault("history_generation",c["generation"])
        if old and (target_changed or c["constraints"]!=old.get("constraints","") or (old["mode"]=="change" and c["mode"]=="inspect")):
            c["generation"]+=1
            if target_changed:
                c["history_generation"]=c["generation"]
            invalidate(old["id"])
        return db.put("chat",c) if old else db.new("chat",**c)


def invalidate(key):
    for t in db.all("task",chat_id=key):
        if t["status"]=="awaiting_confirmation" or (t["status"]=="queued" and t.get("confirmed_at")):
            jobs.save(t,status="stale",result={"detail":"会话目标或权限已变化"})


@api.get("/chats")
def chats(q:str=""):
    return [c for c in db.all("chat") if q.casefold() in c["title"].casefold()]


@api.post("/chats")
def add_chat(d:dict):
    return chat_config(d)


@api.patch("/chats/{key}")
def edit_chat(key:str,d:dict):
    return chat_config(d,db.get("chat",key))


@api.delete("/chats/{key}")
def delete_chat(key:str):
    with db.atomic():
        db.get("chat",key);invalidate(key)
        for m in db.all("message",chat_id=key):
            db.delete("message",m["id"])
        db.delete("chat",key)
    return {"deleted":True}


@api.get("/chats/{key}/context")
def chat_context(key:str):
    c=db.get("chat",key)
    m=db.get("model",c["model_id"]) if c.get("model_id") else next(iter(db.all("model",is_default=True)),None)
    if not m:
        error("请先选择模型")
    return context(db,c,m)


@api.get("/chats/{key}/messages")
def messages(key:str):
    db.get("chat",key)
    return list(reversed(db.all("message",chat_id=key)))


@api.post("/chats/{key}/messages")
def send(key:str,d:dict):
    fields(d,"content report_id request_id")
    return ai.send(key,d)


app.include_router(api)
