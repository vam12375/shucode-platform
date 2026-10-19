#!/usr/bin/env bash
# 可选：启用橙云前，从官方列表刷新 Nginx 的 Cloudflare 来源白名单。
set -euo pipefail

if [[ ${EUID} -ne 0 ]]; then
  echo "请使用 sudo bash 运行此脚本。" >&2
  exit 1
fi

realip_destination=${1:-/etc/nginx/snippets/shucode-cloudflare-realip.conf}
for realip_dependency in curl python3 nginx systemctl; do
  command -v "${realip_dependency}" >/dev/null || {
    echo "缺少命令：${realip_dependency}" >&2
    exit 1
  }
done

# 先确认当前配置可用；不要在已经损坏的 Nginx 配置上覆盖白名单。
nginx -t
realip_tmp_dir=$(mktemp -d)
realip_backup=""
realip_candidate=""
cleanup() {
  rm -rf -- "${realip_tmp_dir}"
  [[ -z ${realip_backup} ]] || rm -f -- "${realip_backup}"
  [[ -z ${realip_candidate} ]] || rm -f -- "${realip_candidate}"
}
trap cleanup EXIT

curl --fail --silent --show-error --proto '=https' --tlsv1.2 --max-time 30 \
  https://www.cloudflare.com/ips-v4 -o "${realip_tmp_dir}/v4"
curl --fail --silent --show-error --proto '=https' --tlsv1.2 --max-time 30 \
  https://www.cloudflare.com/ips-v6 -o "${realip_tmp_dir}/v6"

python3 - "${realip_tmp_dir}" <<'PY'
import ipaddress
import pathlib
import sys

directory = pathlib.Path(sys.argv[1])
networks = []
for filename, version, minimum in [('v4', 4, 10), ('v6', 6, 5)]:
    lines = (directory / filename).read_text().splitlines()
    if not minimum <= len(lines) <= 200:
        raise SystemExit(f'官方 {filename} 列表数量异常，保留旧配置')
    for line in lines:
        network = ipaddress.ip_network(line.strip(), strict=True)
        if network.version != version or network.prefixlen == 0 or not network.is_global:
            raise SystemExit('官方列表包含异常网段，保留旧配置')
        networks.append(network)
if len(set(networks)) != len(networks):
    raise SystemExit('官方列表包含重复网段，保留旧配置')

text = '# Generated from https://www.cloudflare.com/ips-v4 and /ips-v6\n'
text += ''.join(f'set_real_ip_from {network};\n' for network in networks)
text += 'real_ip_header CF-Connecting-IP;\nreal_ip_recursive on;\n'
(directory / 'generated.conf').write_text(text)
PY

install -d -m 755 "$(dirname -- "${realip_destination}")"
if [[ -f ${realip_destination} ]]; then
  realip_backup=$(mktemp "${realip_destination}.previous.XXXXXX")
  cp -p -- "${realip_destination}" "${realip_backup}"
fi
realip_candidate=$(mktemp "${realip_destination}.next.XXXXXX")
install -m 644 "${realip_tmp_dir}/generated.conf" "${realip_candidate}"
mv -f -- "${realip_candidate}" "${realip_destination}"
realip_candidate=""

restore_previous() {
  if [[ -n ${realip_backup} ]]; then
    mv -f -- "${realip_backup}" "${realip_destination}"
    realip_backup=""
  else
    rm -f -- "${realip_destination}"
  fi
}

if ! nginx -t; then
  restore_previous
  echo "新白名单未通过 nginx -t，已恢复旧配置。" >&2
  exit 1
fi
if ! systemctl reload nginx; then
  restore_previous
  if nginx -t; then
    systemctl reload nginx || true
  fi
  echo "Nginx 重载失败，已恢复旧白名单；请检查服务状态。" >&2
  exit 1
fi
echo "Cloudflare 来源白名单已更新并重载。"
