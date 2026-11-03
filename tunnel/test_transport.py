"""Opt-in real-binary acceptance, no cloud or real credentials. Run in an isolated network.
python -m tunnel.test_transport --bin build/native
"""
import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
from urllib.request import urlopen
from .gateway import Gateway


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        if isinstance(self.request, tuple):
            packet, sock = self.request
            sock.sendto(self.server.prefix + packet, self.client_address)
        else:
            packet = self.request.recv(128)
            self.request.sendall(self.server.prefix + packet)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def poll(fn, timeout=45):
    until = time.monotonic() + timeout
    last = None
    while time.monotonic() < until:
        try:
            result = fn()
            if result:
                return result
        except (RuntimeError, OSError) as exc:
            last = type(exc).__name__
        time.sleep(.35)
    raise AssertionError(f"timed out: {fn.__name__} ({last})")


def message(port, udp=False, timeout=1.5):
    nonce = secrets.token_hex(8).encode()
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM if udp else socket.SOCK_STREAM) as s:
        s.settimeout(timeout)
        s.connect(("127.0.0.1", port))
        s.sendall(nonce)
        data = s.recv(256)
        return data[:-len(nonce)] if data.endswith(nonce) else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin", required=True, type=Path)
    args = parser.parse_args()
    binary = args.bin.resolve()
    ext = ".exe" if os.name == "nt" else ""
    agent, manager = binary / ("gostc" + ext), binary / ("tunnel-control" + ext)
    for p in (agent, manager):
        if not p.is_file():
            raise SystemExit(f"Missing binary: {p}")
    with tempfile.TemporaryDirectory() as tmp, ExitStack() as stack:
        def process(exe, *params, **settings):
            proc = subprocess.Popen([str(exe), *params], env={**os.environ, **settings},
                                   cwd=tmp, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            def stop():
                if proc.poll() is None:
                    proc.terminate()
                    try: proc.wait(5)
                    except subprocess.TimeoutExpired: proc.kill(); proc.wait()
            stack.callback(stop)
            return proc
        def target(prefix):
            tcp = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Echo)
            udp = socketserver.ThreadingUDPServer(tcp.server_address, Echo)
            for s in (tcp, udp):
                s.prefix = prefix
                stack.callback(s.server_close)
                threading.Thread(target=s.serve_forever, daemon=True).start()
                stack.callback(s.shutdown)
            return tcp.server_address[1]

        a, b = target(b"one:"), target(b"two:")
        ctrl, frp, public = (free_port() for _ in range(3))
        assert len({ctrl, frp, public, a, b}) == 5
        addr = "127.0.0.1:" + str(ctrl)
        gate = Gateway()
        gate.secret = secrets.token_urlsafe(40)
        gate.url = "http://" + addr
        process(manager, "-p", str(Path(tmp)), GOSTC_CONTROL_SECRET=gate.secret, GOSTC_ADMIN_ADDR=addr)
        poll(lambda: gate.call("node", "list")["status"] == "observed")
        node = gate.call("node", "create", dict(name="qa-node", address=f"127.0.0.1:{frp}", forward_ports=str(public)))["data"]["id"]
        cli = gate.call("client", "create", {"name": "qa-client"})["data"]["id"]
        for kind, key in (("node", node), ("client", cli)):
            token = gate._find(kind, key)["key"]
            process(agent, "-role", kind, "-addr", addr, "-tls=false", GOSTC_AGENT_KEY=token)
            poll(lambda: gate.call(kind, "check", {"id": key})["data"]["online"])
        print("node and client registered", flush=True)
        data = dict(name="qa-rule", node_id=node, client_id=cli, public_port=public, target_host="127.0.0.1", target_port=a)
        rule = gate.call("forward", "create", data)["data"]["id"]
        def matches_one(): return message(public) == b"one:" and message(public, True) == b"one:"
        poll(matches_one)
        poll(lambda: gate.call("forward", "check", {"id":rule})["data"]["applied"] == "applied")
        print("TCP/UDP and client receipt verified", flush=True)
        gate.call("forward", "update", {"id":rule, "target_port":b})
        def matches_two(): return message(public) == b"two:" and message(public, True) == b"two:"
        poll(matches_two)
        gate.call("forward", "enable", {"id":rule, "enabled":False})
        def closed():
            try: return not message(public)
            except OSError: return True
        poll(closed)
        gate.call("forward", "enable", {"id":rule, "enabled":True})
        poll(matches_two)
        del data["public_port"]
        peer = gate.call("p2p", "create", data)["data"]["id"]
        def visitor():
            key = gate._find("p2p",peer)["vKey"]
            with urlopen(gate.url+"/api/v1/public/frp/visitorP2P?key="+key,timeout=4) as resp:
                return resp.read().lower()
        assert b"_p2pxtcp" in visitor() and b"_p2pstcp" not in visitor()
        gate.call("p2p","update",{"id":peer,"allow_relay":True})
        assert b"_p2pstcp" in visitor()
        poll(lambda:gate.call("p2p","check",{"id":peer})["data"]["applied"]=="applied")
        local = free_port()
        process(agent, "-role", "visitor", "-addr", addr, "-tls=false", "-port", str(local),
                GOSTC_AGENT_KEY=gate._find("p2p",peer)["vKey"])
        def private_service(): return message(local, timeout=6) == b"one:"
        poll(private_service)
        print("authenticated visitor delivered target service response",flush=True)
        gate.call("node","update",{"id":node,"disable_relay":True})
        assert b"_p2pstcp" not in visitor()
        gate.call("forward","delete",{"id":rule})
        poll(closed)
        gate.call("p2p","delete",{"id":peer})
        assert not gate.call("forward","list")["data"] and not gate.call("p2p","list")["data"]
        print("edit/stop/start/delete verified; P2P configuration and relay gates verified (not cross-NAT connectivity)", flush=True)


if __name__ == "__main__":
    main()
