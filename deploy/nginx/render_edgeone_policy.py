#!/usr/bin/env python3
"""Validate an exported origin IP list and render candidate Nginx policies."""

import argparse
import ipaddress
from pathlib import Path


def parse_networks(text):
    networks = set()
    for line in text.splitlines():
        line = line.partition("#")[0].strip()
        if not line:
            continue
        network = ipaddress.ip_network(line, strict=True)
        if (network.prefixlen == 0 or not network.is_global
                or network.is_multicast or network.is_loopback
                or network.is_link_local or network.is_reserved):
            raise ValueError("Origin IP list must contain only specific public unicast networks")
        networks.add(network)
    if not networks or len(networks) > 4096:
        raise ValueError("Origin IP list is empty or unexpectedly large")
    return sorted(networks, key=lambda item: (item.version, int(item.network_address), item.prefixlen))


def render(networks, client_ip_header):
    if client_ip_header not in ("X-Forwarded-For", "EO-Connecting-IP"):
        raise ValueError("Unsupported client IP header")
    # real_ip 会改写 remote_addr；准入必须检查原始 TCP 对端，不能用访客头冒充回源节点。
    geo = "geo $realip_remote_addr $mendao_edgeone_origin_allowed {\n    default 0;\n    127.0.0.1/32 1;\n    ::1/128 1;\n"
    geo += "".join(f"    {network} 1;\n" for network in networks)
    geo += "}\n"
    realip = "".join(f"set_real_ip_from {network};\n" for network in networks)
    realip += f"real_ip_header {client_ip_header};\nreal_ip_recursive on;\n"
    gate = "if ($mendao_edgeone_origin_allowed = 0) { return 444; }\n"
    return {"edgeone-origin-geo.conf": geo, "edgeone-realip.conf": realip, "edgeone-origin-gate.conf": gate}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input", type=Path, help="Trusted EdgeOne export, one IP/CIDR per line")
    parser.add_argument("output", type=Path, help="New candidate directory; existing directories are never overwritten")
    parser.add_argument("--client-ip-header", required=True, choices=["X-Forwarded-For", "EO-Connecting-IP"])
    args = parser.parse_args()
    try:
        files = render(parse_networks(args.input.read_text()), args.client_ip_header)
        # 只生成待审核文件，不安装配置、不重载服务，也不修改防火墙。
        args.output.mkdir(parents=True, exist_ok=False)
        for name, content in files.items():
            (args.output / name).write_text(content)
    except (OSError, ValueError) as error:
        parser.exit(1, f"Candidate generation failed: {error}\n")
    print(f"Generated {len(files)} candidate files in {args.output}")


if __name__ == "__main__":
    main()
