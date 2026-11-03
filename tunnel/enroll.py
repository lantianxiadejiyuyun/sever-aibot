"""Explicit local administrator-only secret retrieval. Never available as an AI tool."""
import argparse
import sys
from .gateway import Gateway


def main():
    p = argparse.ArgumentParser(description="在受控终端查看登记信息（勿复制到聊天或日志）")
    p.add_argument("kind", choices=["node", "client", "p2p"])
    p.add_argument("id")
    a = p.parse_args()
    if not sys.stdout.isatty():
        p.exit(1, "只允许在交互终端查看密钥。\n")
    row = Gateway()._find(a.kind, a.id)
    print(row["vKey" if a.kind == "p2p" else "key"])


if __name__ == "__main__":
    main()
