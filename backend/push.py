"""Encrypted Web Push subscriptions, stable VAPID identity, isolated delivery worker."""
import base64
import json
import os
import re
import threading
import time
from urllib.parse import urlsplit

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec
import http_ece
import httpx
from py_vapid import Vapid
from .store import digest, error


def b64(raw):
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def unb64(value,length):
    if not isinstance(value,str) or not re.fullmatch(r"[A-Za-z0-9_-]+={0,2}",value) or len(value)>100:
        error("订阅公钥格式错误")
    raw=base64.urlsafe_b64decode(value.rstrip("=")+"===")
    if len(raw)!=length:
        error("订阅密钥长度错误")
    return raw


def subject():
    value=os.getenv("AIBOT_VAPID_SUBJECT","").strip()
    if re.fullmatch(r"mailto:[^\s@]+@[^\s@]+\.[^\s@]+|https://[A-Za-z0-9.-]+(?:/[A-Za-z0-9._~/-]*)?",value):
        return value
    return None


def key(db):
    if not subject():
        return {"enabled":False,"public_key":None,"reason":"尚未配置有效的 AIBOT_VAPID_SUBJECT"}
    with db.atomic():
        public=db.get("push_key","v1",False)
        if not public:
            private=ec.generate_private_key(ec.SECP256R1())
            db.secret("vapid:v1",b64(private.private_numbers().private_value.to_bytes(32,"big")))
            raw=private.public_key().public_bytes(serialization.Encoding.X962,serialization.PublicFormat.UncompressedPoint)
            public=db.put("push_key",{"id":"v1","public_key":b64(raw)})
    return {"enabled":True,"public_key":public["public_key"],"reason":None}


def endpoint(value):
    if not isinstance(value,str) or len(value)>4096 or not value.isascii() or re.search(r"[\x00-\x20\\#\x7f]",value):
        error("无效推送地址")
    try:
        u=urlsplit(value)
        host=u.hostname or ""
        valid=(u.scheme=="https" and u.netloc==host and u.port is None and (
            (host=="fcm.googleapis.com" and u.path.startswith(("/fcm/send/","/wp/"))) or
            (host=="updates.push.services.mozilla.com" and u.path.startswith(("/wpush/","/push/"))) or
            (host=="web.push.apple.com" and len(u.path)>1) or
            (re.fullmatch(r"(?:[a-z0-9-]+\.)+notify\.windows\.com",host) and (len(u.path)>1 or u.query))))
    except ValueError:
        valid=False
    if not valid:
        error("仅接受已知浏览器推送服务的 HTTPS 订阅地址")
    return value


def subscribe(db,data):
    if not key(db)["enabled"]:
        error("Web Push 未启用",409)
    ep=endpoint(data.get("endpoint"));keys=data.get("keys")
    try:
        if not isinstance(keys,dict):
            raise ValueError()
        raw=unb64(keys.get("p256dh"),65)
        ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(),raw)
        auth=unb64(keys.get("auth"),16)
    except ValueError:
        error("无效的 Web Push 加密密钥")
    sid=digest(ep)
    with db.atomic():
        if not db.get("push_subscription",sid,False) and len(db.all("push_subscription"))>=64:
            error("订阅数量已达上限",409)
        db.secret("push:"+sid,{"endpoint":ep,"keys":{"auth":b64(auth),"p256dh":b64(raw)}})
        db.put("push_subscription",{"id":sid})
    return {"subscribed":True}


def unsubscribe(db,ep):
    sid=digest(endpoint(ep))
    with db.atomic():
        db.delete("push_subscription",sid);db.delete("vault","push:"+sid)
    return {"subscribed":False}


def enqueue(db,n):
    # Called in the notification transaction; never performs network I/O.
    if subject():
        for sub in db.all("push_subscription"):
            key=n["id"]+":"+sub["id"]
            if not db.get("push_delivery",key,False):
                db.put("push_delivery",{"id":key,"status":"pending","sub":sub["id"],"event":n["id"],"attempt":0,"at":0})


def deliver(db,sub,event):
    ep=endpoint(sub["endpoint"])
    private=ec.derive_private_key(int.from_bytes(unb64(db.secret("vapid:v1"),32),"big"),ec.SECP256R1())
    headers=Vapid(private_key=private).sign({"sub":subject(),"aud":"https://"+urlsplit(ep).hostname,
                                              "exp":int(time.time())+3600})
    headers.update({"Content-Type":"application/octet-stream","Content-Encoding":"aes128gcm","TTL":"3600",
                    "Topic":digest(event["id"])[:32]})
    if urlsplit(ep).hostname.endswith(".notify.windows.com"):
        headers["X-WNS-Type"]="wns/raw"
    payload={"id":event["id"],"title":"巡检异常" if event["after"]=="abnormal" else "巡检已恢复",
             "body":"打开面板查看检查证据。","url":"/","tag":event["id"],
             **{k:event.get(k) for k in ("host_id","service_id","report_id")}}
    body=http_ece.encrypt(json.dumps(payload,ensure_ascii=False).encode(),private_key=ec.generate_private_key(ec.SECP256R1()),
                          dh=unb64(sub["keys"]["p256dh"],65),auth_secret=unb64(sub["keys"]["auth"],16),version="aes128gcm")
    with httpx.Client(timeout=5,trust_env=False,follow_redirects=False) as client:
        return client.post(ep,content=body,headers=headers).status_code


def process_one(db):
    if not subject():
        return False
    with db.atomic():
        job=next((j for j in reversed(db.all("push_delivery",status="pending")) if j["at"]<=time.time()),None)
        if not job:
            return False
        # Do not replay an ambiguous send on a worker/process crash.
        job.update(status="sending",attempt=job["attempt"]+1);db.put("push_delivery",job)
    code=0
    try:
        if db.get("push_subscription",job["sub"],False):
            sub=db.secret("push:"+job["sub"])
            code=deliver(db,sub,db.get("notification",job["event"]))
    except Exception:
        pass  # No endpoint, subscription key, payload or exception details in logs.
    with db.atomic():
        if code in (404,410):
            db.delete("push_subscription",job["sub"]);db.delete("vault","push:"+job["sub"])
        job["status"]="accepted" if 200<=code<300 else "failed"
        if code in (429,503) and job["attempt"]<3:
            job.update(status="pending",at=time.time()+60*job["attempt"])
        job["code"]=code
        db.put("push_delivery",job)
    return True


class Sender:
    def __init__(self,db):
        self.db=db;self.stop=threading.Event()
        self.thread=threading.Thread(target=self.loop,daemon=True,name="web-push")
    def start(self):
        self.thread.start()
    def close(self):
        self.stop.set();self.thread.join()
    def loop(self):
        while not self.stop.is_set():
            try:
                if process_one(self.db):
                    continue
            except Exception:
                pass
            self.stop.wait(2)
