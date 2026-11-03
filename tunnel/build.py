"""Build the actual upstream gostc client and our minimal manager for this platform."""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile

PIN = "16671e05b8c0e5d32db0146db862bc71c35e39ee"
UPSTREAM = "https://github.com/SianHH/gostc-open.git"
HERE = Path(__file__).resolve().parent


def run(*args, cwd=None, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--output", default="build/bin")
    p.add_argument("--go", default="go")
    p.add_argument("--source", help="optional local clone containing the pinned commit")
    p.add_argument("--os", choices=["linux", "windows", "darwin"])
    p.add_argument("--arch", choices=["amd64", "arm64"])
    a = p.parse_args()
    dest = Path(a.output).resolve()
    dest.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0")
    if a.os:
        env["GOOS"] = a.os
    if a.arch:
        env["GOARCH"] = a.arch
    suffix = ".exe" if env.get("GOOS", "windows" if os.name == "nt" else "linux") == "windows" else ""
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp) / "src"
        run("git", "-c", "core.autocrlf=false", "init", "-q", str(root))
        run("git", "fetch", "-q", "--depth=1", a.source or UPSTREAM, PIN, cwd=root)
        run("git", "-c", "core.autocrlf=false", "checkout", "-q", "--detach", "FETCH_HEAD", cwd=root)
        run("git", "apply", "--whitespace=nowarn", str(HERE / "gostc-open.patch"), cwd=root)
        for sub, name, target in [("server", "tunnel-control", "."), ("client", "gostc", "./aiagent")]:
            run(a.go, "build", "-p", "4", "-trimpath", "-ldflags=-s -w", "-o",
                str(dest / (name + suffix)), target, cwd=root / sub, env=env)


if __name__ == "__main__":
    main()
