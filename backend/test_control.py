"""Focused API, transport, confirmation and persistence tests. No operator keys are read."""
import io
import json
import os
import socket
import shlex
import sqlite3
import subprocess
import sys
import runpy
from pathlib import Path
import tempfile
import threading
import time
import select
import unittest
from contextlib import nullcontext
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer

from cryptography.fernet import Fernet
from fastapi.testclient import TestClient
import paramiko
from . import main, inspection, sshops
from .store import error


class Control(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.env=patch.dict(os.environ,{"AIBOT_ADMIN_PASSWORD":"test-only-admin-secret","AIBOT_MASTER_KEY":Fernet.generate_key().decode(),
                                      "AIBOT_DB":os.path.join(self.temp.name,"state.db"),"AIBOT_EXTERNAL_PROBE":"0"})
        self.env.start()
        self.client=TestClient(main.app,base_url="https://testserver",raise_server_exceptions=False)
        with patch("backend.push.Sender.loop",lambda sender:sender.stop.wait()):
            self.client.__enter__()
        self.login()

    def tearDown(self):
        self.client.__exit__(None,None,None);self.env.stop();self.temp.cleanup()

    def login(self):
        r=self.req("post","login",{"password":"test-only-admin-secret"})
        self.headers={"X-CSRF-Token":r["csrf"]}

    def req(self,method,path,data=None,status=200):
        r=self.client.request(method,"/api/"+path,json=data,headers=getattr(self,"headers",{}))
        self.assertEqual(status,r.status_code,(method,path,r.text))
        return r.json()

    def host(self,name="app"):
        return self.req("post","hosts",{"name":name,"address":"127.0.0.1","port":1})

    def wait(self,key):
        for _ in range(200):
            t=self.req("get","tasks/"+key)
            if t["status"] not in ("queued","running"):
                return t
            time.sleep(.02)
        self.fail("task did not complete")

    def test_session_csrf_and_throttle(self):
        self.assertEqual(self.client.post("/api/hosts",json={}).status_code,403)
        self.assertEqual(self.client.post("/api/hosts",json={},headers={**self.headers,"Origin":"https://other"}).status_code,403)
        self.assertEqual(self.client.post("/api/login",content="x"*132000).status_code,413)
        self.req("post","logout",{});self.req("get","tasks",status=401)
        for _ in range(10):
            self.req("post","login",{"password":"wrong"},401)
        self.req("post","login",{"password":"test-only-admin-secret"},429)

    def test_real_ssh_trust_before_auth_and_encrypted_import(self):
        # Only ephemeral keys generated in memory for this isolated protocol test.
        key=paramiko.RSAKey.generate(2048); hostkey=paramiko.RSAKey.generate(2048)
        text=io.StringIO();key.write_private_key(text,password="ephemeral-test-passphrase")
        cred=self.req("post","credentials",{"name":"fixture","private_key":text.getvalue(),"passphrase":"ephemeral-test-passphrase","purpose":"inspect"})
        self.assertNotIn("private_key",cred)
        auth=[]
        class Server(paramiko.ServerInterface):
            def check_auth_publickey(self,user,k):
                auth.append(user)
                return paramiko.AUTH_SUCCESSFUL if k==key and user=="observer" else paramiko.AUTH_FAILED
        with socket.socket() as sock:
            sock.bind(("127.0.0.1",0));sock.listen();sock.settimeout(.2)
            stop=threading.Event()
            def serve():
                while not stop.is_set():
                    try:
                        conn,_=sock.accept()
                    except socket.timeout:
                        continue
                    tr=paramiko.Transport(conn);tr.add_server_key(hostkey)
                    try:
                        tr.start_server(server=Server())
                        for _ in range(150):
                            if not tr.is_active():break
                            time.sleep(.003)
                    except (EOFError,OSError,paramiko.SSHException): pass
                    finally:tr.close()
            th=threading.Thread(target=serve,daemon=True);th.start()
            try:
                h=self.req("post","hosts",{"name":"ssh","address":"127.0.0.1","port":sock.getsockname()[1]})
                p="hosts/"+h["id"]
                self.req("post",p+"/bind",{"credential_id":cred["id"],"username":"observer","purpose":"inspect"})
                self.req("post",p+"/test",{},409); self.assertEqual(auth,[])
                self.req("post",p+"/trust",{"fingerprint":"SHA256:wrong"},409);self.assertEqual(auth,[])
                observed=self.req("post",p+"/fingerprint",{});self.assertFalse(observed["trusted"])
                self.req("post",p+"/trust",{"fingerprint":observed["fingerprint"]})
                self.assertTrue(self.req("post",p+"/test",{})["connected"]); self.assertEqual(auth,["observer"])
                hostkey=paramiko.RSAKey.generate(2048)
                self.req("post",p+"/test",{},409);self.assertEqual(auth,["observer"])
                self.req("post",p+"/trust",{"fingerprint":sshops.fp(hostkey)})
                self.req("post","credentials/"+cred["id"]+"/disable",{})
                self.req("post",p+"/test",{},403);self.assertEqual(auth,["observer"])
            finally:
                stop.set();th.join(2)
        persisted=" ".join(str(b) for (b,) in main.db.conn.execute("SELECT body FROM records"))
        self.assertNotIn("BEGIN RSA PRIVATE KEY",persisted)
        self.assertNotIn("ephemeral-test-passphrase",persisted)

    def test_inspection_loss_recovery_and_no_notifications_on_repeated_fault(self):
        h=self.host();s=self.req("post","services",{"host_id":h["id"],"name":"orders"})
        d={"host_id":h["id"],"service_id":s["id"]}
        def run():
            t=self.wait(self.req("post","inspections",d)["id"])
            self.assertEqual(t["status"],"succeeded",t)
            return self.req("get","reports/"+t["result"]["report_id"])
        with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.run",return_value=(0,"sample")):
            run()
        old=self.req("get","monitor?host_id="+h["id"])["latest"]
        r=run();run()
        snap=self.req("get","monitor?host_id="+h["id"])["latest"]
        self.assertTrue(snap["stale"]);self.assertEqual(snap["collected_at"],old["collected_at"]);self.assertEqual(snap["metrics"],old["metrics"])
        self.assertTrue(any(i["status"]=="unverified" and i["name"].startswith("public:") for i in r["items"]))
        self.assertEqual(sum(n["name"]=="ssh" for n in self.req("get","notifications")),1)
        with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.run",return_value=(0,"sample")):
            run()
        self.assertEqual(sum(n["name"]=="ssh" for n in self.req("get","notifications")),2)

    def test_rule_confirm_stale_and_idempotent(self):
        h=self.host(); s=self.req("post","services",{"host_id":h["id"],"name":"orders"})
        d={"host_id":h["id"],"service_id":s["id"]}
        chat=self.req("post","chats",{**d,"mode":"inspect"})
        request={**d,"chat_id":chat["id"],"id":"r1","data":{"enabled":False},"request_id":"once"}
        config={"id":"r1","version":"a","enabled":True,"name":"rule","public_port":8080}
        writes=[]
        def gw(k,a,args):
            if a=="enable":
                self.assertEqual(args["version"],config["version"])
                writes.append(args.copy());config.update(enabled=args["enabled"],version="new")
            return {"data":config.copy(),"status":"observed" if a=="check" else "pending_apply"}
        main.db.put("binding",{"id":"forward:r1","remote_id":"r1","upstream_id":"r1","kind":"forward",**d})
        with patch("backend.jobs.gateway",side_effect=gw):
            self.req("post","tunnels/forward/enable",request,409)
            self.req("patch","chats/"+chat["id"],{"mode":"change"})
            t=self.req("post","tunnels/forward/enable",request)
            self.assertEqual(writes,[]);config["version"]="b"
            self.req("post","tasks/"+t["id"]+"/confirm",{},409)
            req={**request,"request_id":"cached"}
            t=self.req("post","tunnels/forward/enable",req)
            self.req("patch","chats/"+chat["id"],{"service_id":None})
            self.req("post","tasks/"+t["id"]+"/confirm",{},409)
            self.req("patch","chats/"+chat["id"],d)
            req["request_id"]="confirmed"
            t=self.req("post","tunnels/forward/enable",req);self.req("post","tasks/"+t["id"]+"/confirm",{})
            self.assertEqual(self.wait(t["id"])["status"],"succeeded")
            self.req("post","tasks/"+t["id"]+"/confirm",{})
            self.assertEqual(self.req("post","tunnels/forward/enable",req)["id"],t["id"]);self.assertEqual(len(writes),1)

    def test_deploy_verified_only_after_success_and_no_repeat_after_restart(self):
        h=self.host();h["bindings"]["deploy"]={"credential_id":"deploy","username":"operator"};main.db.put("host",h)
        s=self.req("post","services",{"host_id":h["id"],"name":"api","directory":"/srv/api","compose_service":"api","health_url":"http://127.0.0.1:8080/"})
        before={"image":"api:v1","image_id":"sha:1","container":"abcd","ports":[8080],"running":True}
        def execute(tr,s,image,b,step,config_hash):
            step("update")
            if image.endswith("bad"):
                error("健康检查未通过")
            return {**b,"image":image,"health":"HTTP 200"}
        with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.live",return_value=before),patch("backend.sshops.target_hash",return_value="a"*64),patch("backend.sshops.deploy",side_effect=execute),patch("backend.sshops.health",return_value=("ok","HTTP 200")):
            for tag,status in (("v2","succeeded"),("bad","unknown")):
                d={"host_id":h["id"],"service_id":s["id"],"image":"api:"+tag}
                t=self.req("post","deployments",d);self.req("post","tasks/"+t["id"]+"/confirm",{})
                self.assertEqual(self.wait(t["id"])["status"],status)
        profile=self.req("get","services")[0];self.assertEqual(profile["verified"]["image"],"api:v2")
        self.assertEqual(profile["state"],"待核实");self.assertEqual(profile["expected"]["image"],"api:bad")
        main.jobs.close(); row=main.db.get("task",t["id"]);main.jobs.save(row,status="running")
        jobs=main.Jobs(main.db);jobs.start();jobs.close()
        self.assertEqual(main.db.get("task",row["id"])["status"],"unknown")

    def test_compatible_http_model_and_tool_permission(self):
        received=[];reply={"content":"OK"}
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                payload=json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                received.append((self.path,payload,self.headers.get("Authorization")))
                self.send_response(200);self.end_headers()
                self.wfile.write(json.dumps({"choices":[{"message":reply}]}).encode())
            def log_message(self,*args):pass
        srv=ThreadingHTTPServer(("127.0.0.1",0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start()
        try:
            h=self.host()
            m=self.req("post","models",{"name":"provider","base_url":"http://127.0.0.1:"+str(srv.server_port)+"/v1","model":"fixture","api_key":"fixture-secret","tools":True})
            self.assertNotIn("api_key",m)
            self.req("post","models/"+m["id"]+"/test",{})
            self.assertEqual(received[0][0],"/v1/chat/completions");self.assertEqual(received[0][2],"Bearer fixture-secret")
            c=self.req("post","chats",{"host_id":h["id"],"model_id":m["id"]})
            msg="chats/"+c["id"]+"/messages"
            self.assertEqual(self.req("post",msg,{"content":"检查","request_id":"msg"})["message"]["content"],"OK")
            self.req("post",msg,{"content":"检查","request_id":"msg"})
            self.assertEqual(len(received),2)
            self.assertNotIn("fixture-secret",str(received[-1][1]))
            reply.update(content=None,tool_calls=[{"id":"1","type":"function","function":{"name":"plan_deploy","arguments":"{}"}}])
            self.req("post",msg,{"content":"检查"},403);self.assertEqual(self.req("get","tasks"),[])
            marker="synthetic: test-only\n continuation"
            reply.update(content=None,reasoning_content=marker,tool_calls=[{"id":"L","type":"function","function":{"name":"list_rules","arguments":'{"kind":"forward"}'}}])
            def listing(*args):
                reply.clear();reply["content"]="列表为空"
                return []
            with patch.object(main.jobs,"listing",side_effect=listing):
                self.req("post",msg,{"content":"列出规则"})
            self.assertEqual(received[-1][1]["messages"][-2]["reasoning_content"],marker)
            for (body,) in main.db.conn.execute("SELECT body FROM records"):
                self.assertNotIn("synthetic: test-only",body)
        finally:srv.shutdown();srv.server_close()

    def test_compose_command_boundary_and_verification(self):
        cfg={"directory":"/srv/order app","config_path":"compose.yaml","compose_service":"orders",
             "image_variable":"ORDERS_IMAGE","ports":[8010],"health_url":"http://127.0.0.1:8010/ready"}
        before={"image":"orders:v1","container":"abc","image_id":"sha:old","ports":[8010],"running":True}
        after={**before,"image":"orders:v2","container":"def","image_id":"sha:new","directory":"/srv/order app","config_files":["/srv/order app/compose.yaml"]}
        calls=[]
        def command(tr,cmd,**kwargs):
            calls.append(cmd)
            if "config --format" in cmd:return 0,"a"*64
            if "image inspect" in cmd:return 0,"sha:new"
            return 0,""
        with patch("backend.sshops.run",side_effect=command),patch("backend.sshops.live",side_effect=[before,after]),patch("backend.sshops.health",return_value=("ok","HTTP 200")):
            result=sshops.deploy(object(),cfg,"orders:v2",before,lambda msg:None,"a"*64)
        self.assertEqual(result["image_id"],"sha:new")
        self.assertTrue(any("up -d --no-deps -- orders" in c for c in calls))
        with patch("backend.sshops.run",return_value=(0,"orders:wrong")) as cmd,patch("backend.sshops.live",return_value=before):
            with self.assertRaises(main.HTTPException):
                sshops.deploy(object(),cfg,"orders:v2",before,lambda msg:None,"a"*64)
            self.assertEqual(cmd.call_count,1)

    def test_real_jump_channel_uses_separate_target_account(self):
        # Real SSH over direct-tcpip on loopback. The jump sees only its own auth key.
        nodes=[];seen=[];workers=[]
        for username in ("jump-reader","target-reader"):
            k=paramiko.RSAKey.generate(2048);serverkey=paramiko.RSAKey.generate(2048)
            buf=io.StringIO();k.write_private_key(buf)
            sock=socket.socket();sock.bind(("127.0.0.1",0));sock.listen();sock.settimeout(4)
            nodes.append((username,k,serverkey,buf.getvalue(),sock))
        def server(which):
            username,k,serverkey,_,sock=nodes[which]
            dest=[]
            class Endpoint(paramiko.ServerInterface):
                def check_auth_publickey(self,name,key):
                    seen.append((which,name,key.get_fingerprint()))
                    return paramiko.AUTH_SUCCESSFUL if name==username and key==k else paramiko.AUTH_FAILED
                def check_channel_direct_tcpip_request(self,chanid,origin,destination):
                    if which==0 and destination==nodes[1][4].getsockname():
                        dest.append(destination);return paramiko.OPEN_SUCCEEDED
                    return paramiko.OPEN_FAILED_ADMINISTRATIVELY_PROHIBITED
            tr=None
            try:
                conn,_=sock.accept();tr=paramiko.Transport(conn);tr.add_server_key(serverkey);tr.start_server(server=Endpoint())
                if which==0:
                    channel=tr.accept(4)
                    if channel:
                        with channel,socket.create_connection(dest[0],3) as remote:
                            for _ in range(200):
                                if not tr.is_active():break
                                for src in select.select([channel,remote],[],[],.05)[0]:
                                    data=src.recv(32768)
                                    if not data:return
                                    (remote if src is channel else channel).sendall(data)
                else:
                    for _ in range(100):
                        if not tr.is_active():break
                        time.sleep(.02)
            except (OSError,EOFError):
                # The client tears down both transports immediately after authentication.
                pass
            finally:
                if tr:tr.close()
        try:
            hs=[]
            for i,(user,key,serverkey,pem,sock) in enumerate(nodes):
                th=threading.Thread(target=server,args=(i,),daemon=True);workers.append(th);th.start()
                h=self.req("post","hosts",{"name":user,"address":"127.0.0.1","port":sock.getsockname()[1],"jump_host_id":hs[0]["id"] if hs else None})
                main.db.put("host",{**h,"fingerprint":sshops.fp(serverkey)})
                c=self.req("post","credentials",{"name":user,"purpose":"inspect","private_key":pem})
                hs.append(self.req("post","hosts/"+h["id"]+"/bind",{"purpose":"inspect","username":user,"credential_id":c["id"]}))
            self.assertTrue(self.req("post","hosts/"+hs[1]["id"]+"/test",{})["connected"])
            self.assertEqual(seen,[(i,n[0],n[1].get_fingerprint()) for i,n in enumerate(nodes)])
        finally:
            for t in workers:t.join(5)
            for n in nodes:n[4].close()

    def test_rule_checks_require_matching_endpoint_and_protocol(self):
        cfg={"public_host":"example.com:7000","public_port":18080}
        s={"public_probe":{"host":"example.com","port":18081,"protocol":"tcp"}}
        with patch("backend.inspection.public_probe",return_value=("ok","expected")) as run:
            self.assertEqual(inspection.mapping_probe(s,"forward",cfg)[0],"unverified")
            s["public_probe"]["port"]=18080
            self.assertEqual(inspection.mapping_probe(s,"p2p",cfg)[0],"unverified")
            self.assertEqual(inspection.mapping_probe(s,"forward",cfg)[0],"ok")
            self.assertEqual(run.call_count,1)

    def test_pinned_context_and_interrupted_tool_reply(self):
        h=self.host();other=self.host("other")
        m=self.req("post","models",{"name":"small","model":"fixture","base_url":"http://localhost/v1","capacity":8192,"tools":False})
        chat=self.req("post","chats",{"host_id":h["id"],"model_id":m["id"],"constraints":"只分析当前服务，不重启"})
        cid=chat["id"];ctx="chats/"+cid+"/context";msg="chats/"+cid+"/messages"
        task=main.db.new("task",kind="deploy",action="deploy",status="unknown",host_id=h["id"],service_id=None,chat_id=cid,
                         plan={"after":{"image":"old:v2"}},updated_at="2026-11-03",request_id=cid+":interrupted")
        for _ in range(20):
            main.db.new("message",chat_id=cid,generation=0,role="user",content="history"*80)
        seen=[]
        def complete(db,m,messages,*args):
            seen.append(json.loads(messages[-1]["content"]));return {"content":"答复"}
        with patch("backend.controller.completion",side_effect=complete):
            self.req("post",msg,{"content":"x"*4000})
        context=seen[0]["scope"]
        self.assertEqual(context["constraints"],chat["constraints"])
        self.assertIn(task["id"],str(context["sources"]));self.assertEqual(context["mode"],"inspect")
        self.assertTrue(any(r["name"]=="history_summary" for r in context["sources"]))
        main.db.new("message",role="user",chat_id=cid,content="同一请求",generation=0,model_id=m["id"],request_id="interrupted")
        with patch("backend.controller.completion",side_effect=AssertionError("must not rerun model")):
            self.assertEqual(self.req("post",msg,{"content":"同一请求","request_id":"interrupted"})["task"]["id"],task["id"])
            self.req("post",msg,{"content":"不同请求","request_id":"interrupted"},409)
        self.req("patch","chats/"+cid,{"host_id":other["id"]})
        switched=self.req("get",ctx)
        self.assertNotIn(task["id"],str(switched));self.assertNotIn("historyhistory",str(switched))
        self.assertEqual(switched["constraints"],chat["constraints"])

    def test_preview_drift_invalidates_confirmation_without_changing_archive(self):
        h=self.host();h["bindings"]["deploy"]={"username":"deploy","credential_id":"test"};main.db.put("host",h)
        s=self.req("post","services",{"host_id":h["id"],"name":"api","directory":"/srv/api","compose_service":"api","ports":[80],"health_url":"http://127.0.0.1:80"})
        s.update(verified={"image":"api:v1","image_id":"initial","ports":[80],"directory":"/srv/api"},state="verified")
        main.db.put("service",s)
        live={"image":"api:other","image_id":"different","ports":[8080],"directory":"/srv/other","config_files":["/srv/other/compose.yaml"],"running":True,"container":"container"}
        with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.live",return_value=live),patch("backend.sshops.target_hash",side_effect=["a"*64,"b"*64]),patch("backend.sshops.deploy") as write:
            task=self.req("post","deployments",{"host_id":h["id"],"service_id":s["id"],"image":"api:v2"})
            self.assertEqual(task["plan"]["drift"]["image"],{"last_verified":"api:v1","observed":"api:other"})
            self.assertIn("config_file",task["plan"]["drift"])
            self.req("post","tasks/"+task["id"]+"/confirm",{},409)
            self.assertEqual(main.db.get("task",task["id"])["status"],"stale")
            write.assert_not_called()
        self.assertEqual(main.db.get("service",s["id"]),s)

    def test_external_ssh_public_probe_fails_closed_and_records_source(self):
        h=self.host();node=self.req("post","hosts",{"name":"outside","address":"192.0.2.45","port":22})
        spec={"probe_host_id":node["id"],"host":"127.0.0.1","protocol":"tcp","port":443}
        d={"host_id":h["id"],"name":"app","public_probe":spec}
        self.req("post","services",{**d,"public_probe":{**spec,"probe_host_id":h["id"]}},400)
        for alias in ("localhost","::ffff:127.0.0.1","127.0.0.2"):
            candidate=self.req("post","hosts",{"name":"alias","address":alias,"port":2222})
            self.req("post","services",{**d,"public_probe":{**spec,"probe_host_id":candidate["id"]}},400)
        s=self.req("post","services",d)
        def execute(tr,cmd,**options):
            # Execute the exact standard-library program sent to the remote SSH session.
            exe,flag,code,script,arg=shlex.split(cmd)
            self.assertEqual([exe,flag,code],["/usr/bin/python3","-I","-c"])
            r=subprocess.run([sys.executable,flag,code,script,arg],capture_output=True,text=True,timeout=6)
            return r.returncode,r.stdout
        with patch("backend.sshops.connect",return_value=nullcontext(object())) as conn,patch("backend.sshops.run",side_effect=execute):
            status,ev=inspection.public_probe(s["public_probe"],main.db,h["id"])
            self.assertEqual(status,"unverified");self.assertIn("只允许登记的公网地址",ev)
            self.assertEqual(conn.call_args.args[1]["id"],node["id"]);self.assertEqual(conn.call_args.args[2],"inspect")
        with patch.dict(os.environ,{"AIBOT_EXTERNAL_PROBE":"1"}),patch("backend.sshops.connect",side_effect=OSError()),patch("backend.public_probe.probe") as local:
            self.assertEqual(inspection.public_probe(spec,main.db,h["id"])[0],"unverified");local.assert_not_called()
        self.assertIn(node["id"],inspection.probe_source(spec))

    def test_rule_report_keeps_distinct_evidence_without_checking_other_rules(self):
        h=self.host();s=self.req("post","services",{"host_id":h["id"],"name":"orders"})
        scope={"host_id":h["id"],"service_id":s["id"]}
        for rid in ("one","two"):
            main.db.put("binding",{"id":"forward:"+rid,"remote_id":rid,"upstream_id":rid,"kind":"forward",**scope})
        cfg={"id":"one","version":"v2","node_online":True,"client_online":False,"enabled":True,"applied":"pending",
             "target_host":"192.0.2.2","target_port":8000,"public_port":18080}
        with patch("backend.jobs.gateway",return_value={"data":cfg,"status":"observed"}) as gw,patch("backend.inspection.rule_target",return_value=inspection.item("target","ok","HTTP 200","内网 SSH")):
            t=self.wait(self.req("post","tunnels/forward/check",{**scope,"id":"one"})["id"])
        report=self.req("get","reports/"+t["result"]["report_id"])
        self.assertEqual(report["rule_id"],"forward:one")
        layers={i["name"].split(":")[-1]:i for i in report["items"]}
        self.assertEqual([layers[k]["status"] for k in ("configuration","node_online","client_online","receipt","target","public")],
                         ["ok","ok","abnormal","unverified","ok","unverified"])
        self.assertTrue(all(call.args==("forward","check",{"id":"one"}) for call in gw.call_args_list))
        self.assertEqual(main.db.all("monitor",host_id=h["id"]),[])

    def test_deleted_rule_requires_exact_receipt_then_preserves_history(self):
        h=self.host();s=self.req("post","services",{"host_id":h["id"],"name":"svc"})
        scope={"host_id":h["id"],"service_id":s["id"]}
        expected="2026-11-03T01:00:00.123456789Z"
        seen=[]; rows={}; receipts={}
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                _,_,kind,action=self.path.split("/")
                data=json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                seen.append((kind,action,data))
                if self.headers.get("X-Control-Secret")!="t"*32:
                    self.send_error(403);return
                if action=="page":
                    result={"list":rows[kind],"total":len(rows[kind])}
                elif action=="delete" and data=={"code":"r"}:
                    rows[kind]=[];result={}
                elif action=="receipt" and data=={"code":"r","tag":expected}:
                    result=receipts.get((kind,data["tag"]),{})
                else:
                    self.send_error(400);return
                self.send_response(200);self.end_headers()
                self.wfile.write(json.dumps({"code":0,"data":result}).encode())
            def log_message(self,*args):pass
        srv=ThreadingHTTPServer(("127.0.0.1",0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start()
        try:
            with patch.dict(os.environ,{"GOSTC_CONTROL_SECRET":"t"*32,"GOSTC_CONTROL_URL":"http://127.0.0.1:"+str(srv.server_port)}):
                for kind in ("forward","p2p"):
                    with self.subTest(kind=kind):
                        rows[kind]=[{"code":"r","revision":expected,"name":"fixture","enable":1,"targetIp":"127.0.0.1","targetPort":"80","port":"18080"}]
                        for rid in ("r","legacy"):
                            main.db.put("binding",{"id":kind+":"+rid,"remote_id":rid,"upstream_id":rid,"kind":kind,
                                                   "deleted":rid=="legacy","removed":{"id":rid},"status":"pending_revoke",**scope})
                        task=self.req("post","tunnels/"+kind+"/delete",{"id":"r",**scope})
                        self.req("post","tasks/"+task["id"]+"/confirm",{})
                        self.assertEqual(self.wait(task["id"])["status"],"succeeded")
                        self.assertEqual(main.db.get("binding",kind+":r")["revocation_tag"],expected)
                        path="tunnels/"+kind+"?host_id="+h["id"]+"&service_id="+s["id"]
                        receipt={"applied":"revoked","applied_at":"2026-11-03T01:00:01Z"}
                        for key,value in [(None,None),("old-revision",receipt),(expected,{"applied":"revoked","applied_at":None})]:
                            if key:receipts[kind,key]=value
                            self.assertEqual({b["id"] for b in self.req("get",path)},{"r","legacy"})
                        receipts[kind,expected]=receipt
                        self.assertEqual([b["id"] for b in self.req("get",path)],["legacy"])
                        self.assertEqual([b["id"] for b in self.req("get",path)],["legacy"])
                        self.assertEqual([v for k,a,v in seen if k==kind and a=="receipt"],[{"code":"r","tag":expected}]*4)
                        self.assertEqual(main.db.get("binding",kind+":r")["status"],"revoked")
                        hist=self.req("get","tasks/"+task["id"])["result"]["revocation_receipt"]
                        self.assertEqual((hist["revocation_tag"],hist["applied_at"]),(expected,receipt["applied_at"]))
        finally:
            srv.shutdown();srv.server_close()

    def test_bt_12_read_only_version_two_schemas_empty_and_unavailable(self):
        root=Path(self.temp.name)/"panel"
        (root/"class").mkdir(parents=True);(root/"data"/"db").mkdir(parents=True)
        # Reading a panel module must never import or execute it.
        (root/"class"/"common.py").write_text("raise RuntimeError()\ng.version = '12.0.0'\n")
        def probe(code):
            return subprocess.run([sys.executable,"-c",code,str(root)],capture_output=True,text=True,timeout=5)
        self.assertEqual(probe(inspection.BT_VERSION).stdout.strip(),"12.0.0")
        path=root/"data"/"db"/"firewall.db"
        self.assertNotEqual(probe(inspection.BT_QUERY).returncode,0);self.assertFalse(path.exists())
        con=sqlite3.connect(path)
        con.execute("CREATE TABLE firewall_forward(S_Port,T_Address,T_Port,Protocol)")
        con.execute("CREATE TABLE firewall_trans(start_port,ended_ip,ended_port,protocol)");con.commit()
        self.assertEqual(list(json.loads(probe(inspection.BT_QUERY).stdout).values()),[[],[]])
        con.execute("INSERT INTO firewall_forward VALUES (8443,'10.0.0.8',443,'tcp')")
        con.execute("INSERT INTO firewall_trans VALUES (8053,'10.0.0.9',53,'udp')");con.commit();con.close()
        got=json.loads(probe(inspection.BT_QUERY).stdout)
        self.assertEqual(got["data/db/firewall.db:firewall_forward"],[["tcp",8443,"10.0.0.8",443]])
        self.assertEqual(got["data/db/firewall.db:firewall_trans"],[["udp",8053,"10.0.0.9",53]])
        host=self.host()
        def run(tr,cmd,**kw):
            if "g.version" in cmd or "common.py" in cmd:return 0,"12.0.0"
            if "firewall_forward" in cmd:return 0,json.dumps(got)
            return 1,""
        with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.run",side_effect=run):
            r=inspection.inspect(main.db,host)
        item=next(i for i in r["items"] if i["name"]=="bt_rules")
        self.assertEqual(item["status"],"ok");self.assertEqual(item["evidence"],got)
        with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.run",return_value=(1,"")):
            bad=inspection.inspect(main.db,host)
        self.assertEqual(next(i for i in bad["items"] if i["name"]=="bt_rules")["status"],"unverified")

    def test_bt_helper_real_sqlite_and_only_on_permission_failure(self):
        helper=Path(__file__).with_name("aibot-bt-evidence")
        invalid=subprocess.run([sys.executable,"-IS",str(helper),"/etc/passwd"],capture_output=True)
        self.assertEqual((invalid.returncode,invalid.stdout),(64,b""))
        root=Path(self.temp.name)/"panel"
        (root/"data/db").mkdir(parents=True);(root/"class").mkdir()
        (root/"class/common.py").write_text("raise RuntimeError()\ng.version='12.0.0'")
        dbpath=root/"data/db/firewall.db"
        con=sqlite3.connect(dbpath)
        con.execute("CREATE TABLE firewall_forward(Protocol,S_Port,T_Address,T_Port,secret)")
        con.execute("INSERT INTO firewall_forward VALUES ('tcp',8443,'10.0.0.8',443,'MUST-NOT-RETURN')")
        con.execute("CREATE VIEW firewall_trans AS SELECT 'tcp' protocol,1 start_port,secret ended_ip,2 ended_port FROM firewall_forward")
        con.commit();con.close()
        raw=dbpath.read_bytes()
        collect=runpy.run_path(str(helper))["collect"]
        # No fixture switch on the installed CLI; only relocate in this test process.
        collect.__globals__["ROOT"]=root
        with patch.object(os,"geteuid",return_value=1000,create=True):
            evidence=collect()
        self.assertEqual(evidence,{"version":"12.0.0","rules":{"data/db/firewall.db:firewall_forward":[["tcp",8443,"10.0.0.8",443]]}})
        self.assertEqual(dbpath.read_bytes(),raw)
        h=self.host()
        cmd="/usr/bin/sudo -n -- /usr/local/libexec/aibot-bt-evidence"
        for direct,helper_rc,expected,calls in [(77,0,"ok",1),(77,1,"unverified",1),(1,0,"unverified",0),(0,0,"ok",0)]:
            invoked=[]
            def run(tr,command,**kw):
                if command==cmd:
                    invoked.append(command);return helper_rc,json.dumps(evidence)
                if "common.py" in command:
                    return direct,"12.0.0" if not direct else ""
                if "firewall_forward" in command:
                    return direct,"" if direct else json.dumps(evidence["rules"])
                return 1,""
            with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.run",side_effect=run):
                report=inspection.inspect(main.db,h)
            r=next(i for i in report["items"] if i["name"]=="bt_rules")
            self.assertEqual(r["status"],expected);self.assertEqual(len(invoked),calls)
            if direct==77 and not helper_rc:
                self.assertIn("只读助手",r["source"])

    def test_webpush_encrypted_delivery_subscription_and_recovery(self):
        from . import push
        import http_ece
        from cryptography.hazmat.primitives.asymmetric import ec
        from cryptography.hazmat.primitives import serialization
        # Use no actual push endpoint; exercise real VAPID signing and payload encryption.
        with patch.dict(os.environ,{"AIBOT_VAPID_SUBJECT":""}):
            self.assertFalse(self.req("get","push/key")["enabled"])
        with patch.dict(os.environ,{"AIBOT_VAPID_SUBJECT":"mailto:ops@example.org"}):
            key=self.req("get","push/key");self.assertTrue(key["enabled"])
            recipient=ec.generate_private_key(ec.SECP256R1());auth=os.urandom(16)
            ep="https://web.push.apple.com/test-capability"
            sub={"endpoint":ep,"keys":{"p256dh":push.b64(recipient.public_key().public_bytes(serialization.Encoding.X962,serialization.PublicFormat.UncompressedPoint)),
                                         "auth":push.b64(auth)}}
            for unsafe in ("http://web.push.apple.com/test","https://127.0.0.1/test","https://web.push.apple.com.attacker.example/test","https://web.push.apple.com@localhost/test"):
                self.req("post","push/subscriptions",{**sub,"endpoint":unsafe},400)
            self.req("post","push/subscriptions",sub);self.req("post","push/subscriptions",sub)
            self.assertEqual(len(main.db.all("push_subscription")),1)
            self.assertNotIn("test-capability",str(main.db.conn.execute("SELECT body FROM records").fetchall()))
            h=self.host()
            with patch("backend.sshops.connect",side_effect=OSError()):
                inspection.inspect(main.db,h);inspection.inspect(main.db,h)
            sent=[]
            def send(url,*,content,headers):
                self.assertEqual(url,ep);self.assertTrue(headers["Authorization"].lower().startswith("vapid "))
                payload=json.loads(http_ece.decrypt(content,private_key=recipient,auth_secret=auth,version="aes128gcm"))
                sent.append(payload)
                class Reply:status_code=201
                return Reply()
            with patch("httpx.Client.post",side_effect=send):
                self.assertTrue(push.process_one(main.db));self.assertFalse(push.process_one(main.db))
            self.assertEqual(len(sent),1);self.assertEqual(sent[0]["title"],"巡检异常")
            self.assertEqual(self.req("get","push/key")["public_key"],key["public_key"])
            reopened=main.Store()
            try:
                self.assertEqual(push.key(reopened)["public_key"],key["public_key"])
                self.assertEqual(reopened.secret("vapid:v1"),main.db.secret("vapid:v1"))
            finally:
                reopened.conn.close()
            with patch("backend.sshops.connect",return_value=nullcontext(object())),patch("backend.sshops.run",return_value=(1,"")):
                inspection.inspect(main.db,h)
            with patch("backend.push.deliver",return_value=410):
                self.assertTrue(push.process_one(main.db))
            self.assertEqual(main.db.all("notification")[0]["after"],"ok")
            self.assertEqual(main.db.all("push_subscription"),[])
            self.req("post","push/subscriptions",sub)
            self.assertFalse(self.req("delete","push/subscriptions",{"endpoint":ep})["subscribed"])


if __name__=="__main__":
    unittest.main()
