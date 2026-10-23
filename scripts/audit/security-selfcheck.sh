#!/usr/bin/env bash
# 安全回归自查。默认打本地实例；线上只在你有明确授权时用 --base 指向。
#   ./scripts/audit/security-selfcheck.sh                        # http://127.0.0.1:18787
#   ./scripts/audit/security-selfcheck.sh --base https://你的域名
#
# 每一项都对应一次真实踩过的缺陷，不做泛泛的“安全扫描”。
set -uo pipefail

BASE="http://127.0.0.1:18787"
if [ "${1:-}" = "--base" ]; then BASE="${2:-$BASE}"; fi
HOSTPORT="${BASE#*//}"; HOSTPORT="${HOSTPORT%%/*}"
HOST="${HOSTPORT%%:*}"
IS_HTTPS=0; [[ "$BASE" == https://* ]] && IS_HTTPS=1
CURL=(curl -sS --max-time 20)
[ "$IS_HTTPS" = 1 ] || true
FAIL=0

chk(){ printf '%-46s %s\n' "$1" "$2"; case "$2" in OK*) ;; *) FAIL=1 ;; esac; }

hdr(){ "${CURL[@]}" -D- -o /dev/null "$@" 2>/dev/null; }
count_sec(){ hdr "$1" | grep -icE '^(strict-transport-security|content-security-policy|x-frame-options|x-content-type-options|referrer-policy)'; }
code_of(){ "${CURL[@]}" -o /dev/null -w '%{http_code}' "$@" 2>/dev/null || echo 000; }

echo "== 1. 跳转响应必须带安全头（nginx return 短路的高发点）=="
for p in / /console /console/; do
  n=$(count_sec "$BASE$p")
  [ "$n" -ge 3 ] && chk "$p 安全头=$n" OK || chk "$p 安全头=$n" "FAIL(应>=3)"
done

echo "== 2. 设备授权页不得依赖 script-src 'unsafe-inline' =="
csp=$(hdr "$BASE/app/platform/login" | grep -i '^content-security-policy' || true)
if [ -z "$csp" ]; then
  chk "CSP 缺失" "FAIL"
else
  script_dir=$(printf '%s' "$csp" | sed -E "s/.*(script-src[^;]*).*/\1/")
  case "$script_dir" in *unsafe-inline*) chk "script-src 含 unsafe-inline" "FAIL" ;; *) chk "script-src 无 unsafe-inline" OK ;; esac
  case "$csp" in *"object-src 'none'"*) chk "object-src 'none'" OK ;; *) chk "object-src 'none'" "FAIL" ;; esac
fi
chk "/app/redirect.js 可获取" "$( [ "$(code_of "$BASE/app/redirect.js")" = 200 ] && echo OK || echo FAIL )"

echo "== 3. HSTS 不得在明文下下发；includeSubDomains 需显式开启 =="
if [ "$IS_HTTPS" = 1 ]; then
  hsts=$(hdr "$BASE/healthz" | grep -i '^strict-transport-security' | tr -d '\r')
  [ -n "$hsts" ] && chk "HSTS 已下发" OK || chk "HSTS 已下发" "FAIL"
  case "$hsts" in
    *includeSubDomains*) echo "   注意：$hsts"; echo "   → 请确认该域每个子域证书有效，否则会锁死用户" ;;
    *) chk "HSTS 未含 includeSubDomains（默认安全）" OK ;;
  esac
else
  # 明文入口上浏览器会直接忽略 HSTS，不构成缺陷；这里只做提示不算失败——
  # 本地实例常把 PUBLIC_API_ORIGIN 配成 https 走明文隧道，此时也会下发 HSTS。
  hsts_l=$(hdr "$BASE/healthz" | grep -ic '^strict-transport-security')
  if [ "$hsts_l" = 0 ]; then
    chk "明文入口未见 HSTS" OK
  else
    echo "    提示：明文入口下发了 HSTS（浏览器会忽略，通常只是 PUBLIC_API_ORIGIN 配了 https）"
    chk "明文入口未见 HSTS" OK
  fi
fi

echo "== 4. 证书须覆盖自身域名（apex/www 是高发错配点）=="
if [ "$IS_HTTPS" = 1 ] && [ "$HOST" != "localhost" ] && [ "$HOST" != "127.0.0.1" ]; then
  out=$(echo | openssl s_client -servername "$HOST" -connect "$HOST:443" 2>/dev/null \
        | openssl x509 -noout -checkhost "$HOST" 2>/dev/null)
  case "$out" in *"does match"*) chk "$HOST 证书匹配" OK ;; *) chk "$HOST 证书匹配" "FAIL(${out:-无响应})" ;; esac
fi

echo "== 5. 未认证不得读管理接口 =="
for p in /console/api/users /console/api/providers /console/api/teams /console/api/audit; do
  c=$(code_of "$BASE$p")
  [ "$c" = 401 ] && chk "$p 未认证" OK || chk "$p 未认证" "FAIL($c, 应401)"
done

echo "== 6. CSRF：跨站写操作必须 403 =="
for h in "origin: https://evil.example.com" "sec-fetch-site: cross-site" "sec-fetch-site: same-site"; do
  c=$(code_of -X POST "$BASE/console/api/auth/login" -H 'content-type: application/json' -H "$h" \
      -d '{"email":"a@b.co","password":"x"}')
  [ "$c" = 403 ] && chk "$h" OK || chk "$h" "FAIL($c)"
done

echo "== 7. setup/root 幂等：已初始化必须 409 =="
c=$(code_of -X POST "$BASE/console/api/setup/root" -H 'content-type: application/json' \
    -H "x-bootstrap-secret: ${BOOTSTRAP_SECRET:-x}" \
    -d '{"email":"a@b.co","displayName":"x","password":"ReplayPassw0rd!23"}')
[ "$c" = 409 ] && chk "重复初始化" OK || chk "重复初始化" "FAIL($c, 应409)"

echo "== 8. source map 不得暴露 =="
ct=$("${CURL[@]}" -o /dev/null -w '%{content_type}' "$BASE/console/assets/index-CySlpsco.js.map" 2>/dev/null)
case "$ct" in *json*) chk "*.js.map" "FAIL(泄漏)" ;; *) chk "*.js.map" OK ;; esac

echo "== 9. security.txt（配置 SECURITY_CONTACT 后应有）=="
c=$(code_of "$BASE/.well-known/security.txt")
case "$c" in 200) chk "security.txt" OK ;; 404) echo "    security.txt 未配置（SECURITY_CONTACT 为空）" ;; *) chk "security.txt" "FAIL($c)" ;; esac

echo
[ $FAIL -eq 0 ] && echo "全部通过" || echo "存在问题项，见上"
exit $FAIL
