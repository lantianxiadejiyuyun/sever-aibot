"""Run only an administrator-registered probe, from the selected network."""
import http.client
import ipaddress
import json
import socket
import ssl
import sys


def probe(spec):
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


if __name__=="__main__":
    print(json.dumps(probe(json.loads(sys.argv[1]))))
