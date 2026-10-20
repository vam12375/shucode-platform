# faction168.online：Ubuntu + Nginx 域名接入

本文是服务器手工操作指南，项目目录为 `/opt/shucode-platform`。仓库提供配置文件，不代表已登录服务器、修改 DNS 或完成部署。真实源站公网 IP 从 2026-10-19 起不再写入仓库与文档，下文统一以 `<源站公网IP>` 占位；执行命令前用服务器实际 IP 替换。通过 SSH 使用现有账号和密码执行以下操作，不把密码写进命令或仓库。

此次接入保留现有应用镜像、数据库、账号和密钥，只调整域名、公开地址、端口绑定与反向代理。现有 `www`、MX、TXT 和 Worker 保持原状，不重建整个 DNS 区域。

| 公网入口 | 用途 | Nginx 回源 |
| --- | --- | --- |
| `https://api.faction168.online` | 客户端公开配置、登录流初始化/轮询、配置事件 | `127.0.0.1:8787` |
| `https://console.faction168.online/console/` | 控制台、控制台 API、浏览器设备登录与授权页 | `127.0.0.1:8787` |
| `https://gw.faction168.online` | 模型网关 | `127.0.0.1:8788` |

浏览器设备授权页和控制台共用 **console 域名**；客户端控制面仍使用 api 域名。这样 root 完成 MFA 后可以直接授权设备，cookie 无需扩大到整个 `faction168.online`。

如果服务器位于中国大陆，先确认该域名已满足腾讯云及当地要求的备案条件；DNS 和证书配置不会替代备案。

若使用本轮生成的 `faction168-nginx-config.tar.gz`，通过 SSH 工具的 SFTP 上传到服务器用户家目录。压缩包只含 `nginx/` 配置与本文，不包含密码或证书。先把文件解压到项目的 `deploy/`：

```bash
tar -tzf ~/faction168-nginx-config.tar.gz
sudo install -d -m 755 /opt/shucode-platform/deploy
sudo tar -xzf ~/faction168-nginx-config.tar.gz -C /opt/shucode-platform/deploy
```

之后按下列顺序配置。没有部署项目或没有 `/opt/shucode-platform/.env` 时，先定位真实部署目录；不要用本目录的非秘密示例创建一份缺少原有密钥的新生产配置。

## 1. 确认当前部署和访问条件

以下命令均在服务器的 Bash 中执行；如果 SSH 使用非 22 端口，继续使用实际端口。先保留当前 SSH 会话，再另开一个会话验证可重新登录。

```bash
cd /opt/shucode-platform
test -f .env
docker compose version
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps
sudo ss -ltnp '( sport = :80 or sport = :443 or sport = :8787 or sport = :8788 )'
```

确认这确实是当前部署使用的 Compose 项目与工作目录。如果原部署带有 `-p`、`COMPOSE_PROJECT_NAME` 或其他配置文件，下文所有 Compose 命令必须保留相同项目名和原有配置；不要意外启动第二套服务。

服务器已有 Nginx 时，将本站点加入现有配置，保留其他站点。如果 80/443 由其他进程占用，先确认归属并安排入口切换，不直接杀进程。CI 仅将 `deploy/nginx/` 同步到项目目录，站点安装、证书签发和重载按本文手工完成。

在腾讯云安全组确认：

- 允许现有 SSH 端口，通常为 TCP 22；优先限制到管理者来源地址。
- 允许公网 TCP 80、443；80 用于证书签发、续期和 HTTPS 跳转。
- 8787、8788、5432 不向公网放行。

如果服务器已有 UFW/nftables 规则，保留规则并检查 80/443 的入站放行。本流程不执行 `ufw enable`、清空防火墙或删除数据卷。

## 2. Cloudflare DNS 先全部使用灰云

在 Cloudflare 的 `faction168.online` DNS 页面，仅修改/新增下列记录：

| 类型 | 名称 | 目标 | 代理状态 |
| --- | --- | --- | --- |
| A | `api` | `<源站公网IP>` | DNS only，灰云 |
| A | `console` | `<源站公网IP>` | DNS only，灰云 |
| A | `gw` | `<源站公网IP>` | DNS only，灰云 |

`api` 如果已有旧 A/CNAME，修改对应记录，避免留下互相冲突的目标。此次不为这三个名称配置 AAAA；如果已有 AAAA 指向其他服务器或本机尚未配置 IPv6，移除这三个名称对应的错误 AAAA，保留其他名称的记录。保留已有 `www`、MX、TXT、Worker 和根域名配置。

安装工具并检查公网解析：

```bash
sudo apt-get update
sudo apt-get install -y nginx certbot curl ca-certificates dnsutils tcpdump python3
dig +short A api.faction168.online @1.1.1.1
dig +short A console.faction168.online @1.1.1.1
dig +short A gw.faction168.online @1.1.1.1
dig +short AAAA api.faction168.online @1.1.1.1
dig +short AAAA console.faction168.online @1.1.1.1
dig +short AAAA gw.faction168.online @1.1.1.1
```

三个 A 结果都应为 `<源站公网IP>`，三个 AAAA 应为空。DNS 缓存尚未更新时，先等解析正确，再申请证书。

## 3. 备份并确认镜像版本

先把仓库中的 `deploy/nginx/` 和 `docker-compose.prod.yml` 放到服务器对应位置；如果通过 Git 更新，先检查服务器本地修改，避免覆盖原部署配置。配置目录包含 HTTP 引导配置、TLS 配置、共享 snippets、Cloudflare IP 更新脚本和非秘密环境变量示例。新的生产编排强制 api/gateway 只绑定回环，首次使用前先完成并验证 Nginx 的 HTTPS 入口，再执行服务重建或 CI 发布。

```bash
cd /opt/shucode-platform
test -f deploy/nginx/faction168.online-http.conf
test -f deploy/nginx/faction168.online.conf
test -f deploy/nginx/proxy-common.conf
test -f deploy/nginx/cloudflare-realip.conf
test -f deploy/nginx/faction168.online.env.example
deployment_backup_dir="/root/shucode-domain-backup-$(date +%Y%m%d-%H%M%S)"
sudo install -d -m 700 "$deployment_backup_dir"
sudo cp -a .env "$deployment_backup_dir/platform.env"
sudo cp -a /etc/nginx "$deployment_backup_dir/nginx"
sudo chmod 600 .env
```

记录正在运行的镜像引用和镜像 ID；这些输出不包含应用环境变量：

```bash
for service in api gateway; do
  deployment_container_id="$(docker compose -f docker-compose.yml -f docker-compose.prod.yml ps -q "$service")"
  test -n "$deployment_container_id" || break
  docker inspect --format '{{.Name}} image={{.Config.Image}} id={{.Image}}' "$deployment_container_id"
done
docker compose -f docker-compose.yml -f docker-compose.prod.yml config --images
```

保留当前部署的 `IMAGE_TAG`，尤其是已有的 `sha-...` 标签；不要为了接域名改成 `latest`。确认 Compose 解析出的 api/gateway 镜像引用在本机存在，且对应镜像 ID 与当前容器一致。可对上一步输出的具体镜像引用执行 `docker image inspect <镜像引用> --format '{{.Id}}'`。如引用或 ID 不匹配，先对齐原部署的标签和 Compose 调用方式，再继续。

**域名切换不会自动发布新功能。** 先确认现有 api 镜像含控制台产物，并支持 `PUBLIC_APP_ORIGIN`、`PUBLIC_CONSOLE_ORIGIN`、`CONSOLE_ALLOWED_HOSTS` 及所需的登录/MFA逻辑。仓库里的新角色、权限或安全功能可能尚未构建发布；需要升级时，应另行验证对应镜像和数据库迁移，不在本次切换中临时拉取新版本。

## 4. 合并公开地址和端口配置

在服务器编辑现有 `.env`，参照 `deploy/nginx/faction168.online.env.example` **逐项合并**。不把示例整份复制到 `.env`，不直接追加重复变量，不输出完整 `.env` 到终端记录。

```bash
sudoedit /opt/shucode-platform/.env
```

本次需要的非秘密值为：

```dotenv
PUBLIC_API_ORIGIN=https://api.faction168.online
PUBLIC_APP_ORIGIN=https://console.faction168.online
PUBLIC_CONSOLE_ORIGIN=https://console.faction168.online
GATEWAY_BASE_URL=https://gw.faction168.online
CONSOLE_ALLOWED_HOSTS=console.faction168.online
COOKIE_DOMAIN=
API_HOST_IP=127.0.0.1
GATEWAY_HOST_IP=127.0.0.1
API_HOST_PORT=8787
GATEWAY_HOST_PORT=8788
TRUST_CF_CONNECTING_IP=
```

`API_HOST_IP` / `GATEWAY_HOST_IP` 仅影响基础编排；当前 `docker-compose.prod.yml` 已强制回环绑定，保留这两个值也便于兼容旧编排。若修改宿主端口，同步修改 Nginx 回源端口。

保留已有 `PLATFORM_SECRET_KEY`、`POSTGRES_PASSWORD`、`UPSTREAM_API_KEY`、供应商配置和镜像标签，不重新生成。已有 `RELEASE_BASE_URL` 如果指向可用的包分发服务，继续保留；不要因为切域名而改成尚未提供真实下载的地址，也不要设置强制更新版本号。

`COOKIE_DOMAIN` 留空生成 console 的 host-only cookie。控制台通过相对路径 `/console/api/*` 请求自身域名，设备登录通过 `PUBLIC_APP_ORIGIN` 进入同一域名；不要把前端请求改成跨子域 fetch，也不需要给 cookie 设置父域。

### 找出容器真正看到的反代地址

Nginx 运行在宿主机并连接 `127.0.0.1:8787/8788`，但 Docker 端口转发后，应用通常看到的是 Docker 网桥 gateway，**不能据此把可信代理只填写成 `127.0.0.1`**。api 与 gateway 的网络也可能不同。

先分别检查网络候选地址：

```bash
for service in api gateway; do
  deployment_container_id="$(docker compose -f docker-compose.yml -f docker-compose.prod.yml ps -q "$service")"
  test -n "$deployment_container_id" || break
  docker inspect --format '{{.Name}}{{range $name, $network := .NetworkSettings.Networks}}{{printf "\n%s gateway=%s ip=%s" $name $network.Gateway $network.IPAddress}}{{end}}' "$deployment_container_id"
done
```

需要确认实际来源时，用两个 SSH 终端观察仅包含健康检查的连接。例如 api：

```bash
# 终端 A：宿主机 tcpdump 进入 api 的网络 namespace，最多观察 15 秒。
cd /opt/shucode-platform
deployment_api_id="$(docker compose -f docker-compose.yml -f docker-compose.prod.yml ps -q api)"
deployment_api_pid="$(docker inspect --format '{{.State.Pid}}' "$deployment_api_id")"
sudo timeout 15 nsenter -t "$deployment_api_pid" -n -- tcpdump -nn -i any -c 4 'tcp dst port 8787'
```

```bash
# 终端 B：不携带任何凭据，只触发本机健康检查。
curl -fsS http://127.0.0.1:8787/healthz
```

对 gateway 用相同方法替换服务名为 `gateway`、端口为 `8788`。观察 TCP 的**源地址**，与 Docker inspect 的 gateway 比较，不将应用容器自身 IP 当作代理地址。

在 `.env` 中分别填入 `API_TRUSTED_PROXY_IPS=127.0.0.1,实际API网桥Gateway`、`GATEWAY_TRUSTED_PROXY_IPS=127.0.0.1,实际网关网桥Gateway`；这里的中文占位必须替换成现场确认的精确 IP。若实测来源确实只有 `127.0.0.1`，该项只填 `127.0.0.1` 即可。只接受逗号分隔的精确 IPv4/IPv6，不能填整个私网、CIDR 或 `*`。网络重建后需再次核实。

本方案由 Nginx 清洗客户端地址并覆盖 XFF，应用的 `TRUST_CF_CONNECTING_IP` 始终保持关闭；是否橙云不改变此设置。

校验 Compose 而不打印展开后的秘密：

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml config --quiet
```

使用基础编排加当前生产覆写即可；api/gateway 的发布地址固定为 `127.0.0.1`，即使旧 `.env` 中的 `*_HOST_IP` 为 `0.0.0.0` 也不会公开端口，无需新增 override 文件。

## 5. 先启用 HTTP 引导站点并签发证书

引导配置只提供 ACME webroot 和切换提示，不在明文 HTTP 下开放登录。先使用它，避免完整 TLS 配置引用尚不存在的证书。

```bash
sudo install -d -m 755 /var/www/letsencrypt/.well-known/acme-challenge
sudo install -m 644 /opt/shucode-platform/deploy/nginx/faction168.online-http.conf /etc/nginx/sites-available/faction168.online
sudo ln -sfn /etc/nginx/sites-available/faction168.online /etc/nginx/sites-enabled/faction168.online
sudo nginx -t
sudo systemctl enable nginx
sudo systemctl reload nginx
```

若 Nginx 尚未运行，最后一步改为 `sudo systemctl start nginx`。保留其他站点配置；如发现相同 `server_name` 的旧配置，先备份并解决冲突，再通过 `nginx -t`。

先验证三个域名都能从公网获取 challenge 文件：

```bash
printf '%s\n' 'shucode-acme-ok' | sudo tee /var/www/letsencrypt/.well-known/acme-challenge/shucode-check >/dev/null
curl -fsS http://api.faction168.online/.well-known/acme-challenge/shucode-check
curl -fsS http://console.faction168.online/.well-known/acme-challenge/shucode-check
curl -fsS http://gw.faction168.online/.well-known/acme-challenge/shucode-check
sudo rm /var/www/letsencrypt/.well-known/acme-challenge/shucode-check
```

输出均应为 `shucode-acme-ok`。如果失败，先检查 DNS、80 入站、安全组和 Nginx 匹配，不反复申请证书。

```bash
sudo certbot certonly --webroot \
  -w /var/www/letsencrypt \
  --cert-name faction168.online \
  -d api.faction168.online \
  -d console.faction168.online \
  -d gw.faction168.online
```

按 Certbot 交互填写自己管理的真实邮箱并确认服务条款，不使用示例邮箱。成功后确认以下文件存在：

```bash
sudo test -s /etc/letsencrypt/live/faction168.online/fullchain.pem
sudo test -s /etc/letsencrypt/live/faction168.online/privkey.pem
```

## 6. 安装 HTTPS 配置并重建应用容器

初始的 `cloudflare-realip.conf` 只声明头名称，没有任何 `set_real_ip_from` 信任段，因此灰云下不会相信客户端伪造的 CF 头。

```bash
sudo install -d -m 755 /etc/nginx/snippets
sudo install -m 644 /opt/shucode-platform/deploy/nginx/proxy-common.conf /etc/nginx/snippets/shucode-proxy-common.conf
sudo install -m 644 /opt/shucode-platform/deploy/nginx/gzip.conf /etc/nginx/snippets/shucode-gzip.conf
sudo install -m 644 /opt/shucode-platform/deploy/nginx/cloudflare-realip.conf /etc/nginx/snippets/shucode-cloudflare-realip.conf
sudo install -m 644 /opt/shucode-platform/deploy/nginx/faction168.online.conf /etc/nginx/sites-available/faction168.online
sudo nginx -t
sudo systemctl reload nginx
```

`proxy-common.conf` 不再全局关闭 gzip：console/api 的 server 块 include `shucode-gzip.conf`，gw 的 server 块显式 `gzip off`，避免压缩流式响应。三个 443 server 均启用 `http2 on;`。如果宿主机仍启用 Ubuntu 默认站点，`default_server` 会与本站点的默认拒绝块冲突；先停用默认站点（可回退的软链操作），再安装本站点配置。

确认第 3 节记录的镜像引用与 ID 仍一致，再只重建 api 与 gateway，使它们读取新的公开地址和 loopback 端口绑定：

```bash
cd /opt/shucode-platform
docker compose -f docker-compose.yml -f docker-compose.prod.yml config --quiet
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --no-deps --no-build --pull never api gateway
docker compose -f docker-compose.yml -f docker-compose.prod.yml ps api gateway
sudo ss -ltnp '( sport = :8787 or sport = :8788 or sport = :5432 )'
curl -fsS http://127.0.0.1:8787/healthz
curl -fsS http://127.0.0.1:8788/healthz
```

`--pull never --no-build` 防止本次域名调整顺带更换应用镜像；`--no-deps` 不重启数据库、不触发迁移或种子。如果本地缺失对应镜像，此命令会失败，应先核对原部署版本，不临时换成 `latest`。

8787/8788 应仅绑定 `127.0.0.1`，不能出现 `0.0.0.0` 或 `[::]`。部分 Docker 配置不启动 userland proxy，`ss` 不显示端口时，再用下列命令确认实际 Docker 映射，并从另一台机器检查公网端口不可达：

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml port api 8787
docker compose -f docker-compose.yml -f docker-compose.prod.yml port gateway 8788
```

容器重建后，再检查一次 Docker gateway 与第 4 节一致。这里不执行 `docker compose down`、`down -v`、迁移或 root 初始化；已有 root 继续用原账号登录。

### 系统网络调优（BBR）

上海单机到境外的链路对丢包敏感。安装 `deploy/sysctl/99-shucode-network.conf` 让内核使用 BBR + fq，并在空闲后避免重新慢启动；不改变应用行为：

```bash
sudo install -m 644 /opt/shucode-platform/deploy/sysctl/99-shucode-network.conf /etc/sysctl.d/99-shucode-network.conf
echo tcp_bbr | sudo tee /etc/modules-load.d/tcp_bbr.conf
sudo modprobe tcp_bbr
sudo sysctl --system
sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc
```

预期输出 `bbr` 和 `fq`；已有网卡的 qdisc 可能到重启后才切换为 fq，BBR 本身已由 `tcp_congestion_control` 生效。注意出向带宽仍是云厂商上限（当前 4Mbps），BBR 只改善丢包/排队下的有效吞吐，不会突破带宽封顶。

## 7. 验证域名、会话和模型流

先绕过 DNS 代理直测源站，HTTPS 使用域名验证证书，不使用 `-k`：

```bash
curl -fsS --resolve api.faction168.online:443:<源站公网IP> https://api.faction168.online/healthz
curl -fsS --resolve console.faction168.online:443:<源站公网IP> https://console.faction168.online/healthz
curl -fsS --resolve gw.faction168.online:443:<源站公网IP> https://gw.faction168.online/healthz
curl -I --resolve console.faction168.online:443:<源站公网IP> https://console.faction168.online/console/
curl -I http://api.faction168.online/
curl -I http://console.faction168.online/console/
curl -I http://gw.faction168.online/
curl -I https://api.faction168.online/console/
```

健康检查应成功，console 静态页面应可达，普通 HTTP 应跳转 HTTPS，api 上的 `/console/` 应返回 404。代理配置保留原始 Host 和正确的 `X-Forwarded-Proto`；如果出现循环跳转或 HTTPS POST 被拒，先检查这两个头与 Cloudflare SSL 模式。

验证 HTTP/2 与 gzip（资源哈希随构建变化，从当前页面解析）：

```bash
asset=$(curl -fsS --resolve console.faction168.online:443:<源站公网IP> \
  https://console.faction168.online/console/ | grep -oE '/console/assets/[^" ]+\.js' | head -1)
curl -sSI -H 'Accept-Encoding: gzip' --resolve console.faction168.online:443:<源站公网IP> \
  "https://console.faction168.online$asset" | grep -i -E 'HTTP/|content-encoding|vary'
curl --http2 -sS -o /dev/null -w 'http=%{http_version}\n' --resolve console.faction168.online:443:<源站公网IP> \
  https://console.faction168.online/console/
```

期望看到 `content-encoding: gzip`、`vary: Accept-Encoding` 和 `http=2`。index.html 只有几百字节，低于 `gzip_min_length`，不压缩是正常的；gw 的响应不应出现 `content-encoding`。

用无凭据请求验证控制台同源写入可到达业务路由。以下是假账号，应得到业务层 401，而不是来源校验 403：

```bash
curl -i --resolve console.faction168.online:443:<源站公网IP> \
  https://console.faction168.online/console/api/auth/login \
  -H 'Origin: https://console.faction168.online' \
  -H 'Sec-Fetch-Site: same-origin' \
  -H 'Content-Type: application/json' \
  --data '{"email":"domain-check@example.invalid","password":"invalid-domain-check"}'
```

仅执行一次即可，避免触发登录限流。浏览器中继续完成实际验收：

1. 打开 `https://console.faction168.online/console/`，用原 root 账号登录；如已启用 MFA，完成动态码验证。旧 IP/旧域名的 cookie 不会迁移，需要重新登录。
2. 在浏览器开发者工具确认会话 cookie 为 Secure、HttpOnly，域为 console 主机，不是父域；控制台 API 均访问 console 域名。
3. 客户端控制面地址使用 `https://api.faction168.online`，重新发起一次平台账号登录；`authorize_url` 应进入 `https://console.faction168.online/app/platform/login?...`，已有会话可直接显示授权页。
4. 授权完成后检查下发的网关地址为 `https://gw.faction168.online/v1`（OpenAI 模式），用现有可用模型发起一次正常请求和一次流式请求。流式内容应逐步到达，不能等结束才整段显示。
5. 检查对应网关用量的客户端 IP 是否为实际测试来源，而不是 Docker gateway 或 CF 边缘 IP；API 登录限流也不应把所有用户合并到代理地址。

共享 Nginx 配置已用于 SSE/模型流：上游 HTTP/1.1、关闭代理缓冲与缓存、较长读取超时，并清洗转发头；gw 的 server 块显式 `gzip off`。不要对 SSE、`/console/api/*` 或模型请求启用 Cache Everything。模型请求体可能达 32 MiB，不要被 Nginx 默认的 1 MiB 限制提前拒绝。

## 8. 配置证书续期

HTTP 的 `/.well-known/acme-challenge/` 必须继续直出 webroot，不能被统一跳转、登录校验或其他站点覆盖。安装仅在证书成功更新后执行的 reload hook：

```bash
sudo install -d -m 755 /etc/letsencrypt/renewal-hooks/deploy
sudo tee /etc/letsencrypt/renewal-hooks/deploy/shucode-nginx-reload >/dev/null <<'EOF'
#!/bin/sh
set -eu
/usr/sbin/nginx -t
/bin/systemctl reload nginx
EOF
sudo chmod 755 /etc/letsencrypt/renewal-hooks/deploy/shucode-nginx-reload
sudo certbot renew --cert-name faction168.online --dry-run
systemctl list-timers certbot.timer
```

Ubuntu 的 Certbot 包通常带 `certbot.timer`；如果未启用，可执行 `sudo systemctl enable --now certbot.timer`。确认定时器后，不再额外创建重复 cron。`renew --dry-run` 验证续期挑战，hook 的 Nginx 校验也可单独运行 `sudo /etc/letsencrypt/renewal-hooks/deploy/shucode-nginx-reload`。

> 2026-10-19 实测：域名被腾讯云备案拦截时，HTTP-01 的 `/.well-known/acme-challenge/` 也会被 302 到备案页，`certbot renew --dry-run` 会报 `unauthorized` / `reader size limit exceeded`。备案完成前请改用 **DNS-01**（例如 `certbot-dns-cloudflare` + Cloudflare API Token），或尽快完成备案；否则证书到期后无法自动续期。当前证书有效期至 **2027-01-17**。

## 9. 可选：api 和 console 再开启橙云

> 2026-10-19 定位：HTTP 非根路径被 302 到 `https://dnspod.qcloud.com/static/webblock.html?d=<域名>`，页面标题/按钮为“网站暂时无法访问 / 去腾讯云备案控制台”；HTTPS 的入向 TLS RST 是同一策略的另一种表现。这是**腾讯云未备案/未接入备案拦截**，不是服务器性能或 Nginx 问题。拦截按节点/路径间歇生效（根路径偶尔能到 Nginx），所以会出现“国内有时很快、境外大量重置”的错觉。**先完成备案/接入备案，或换用已备案域名/境外主机；不要在未备案域名上继续调 Nginx。**

先保持全灰完成前述验收；gw 始终保持 DNS only，减少模型长响应受边缘超时影响。

需要为 api/console 开启代理时，先在 Cloudflare 的 SSL/TLS 设置选择 **Full (strict)**，不要使用 Flexible。随后在服务器执行官方 Cloudflare 来源 IP 段更新：

```bash
sudo bash /opt/shucode-platform/deploy/nginx/update-cloudflare-realip.sh /etc/nginx/snippets/shucode-cloudflare-realip.conf
```

脚本从 Cloudflare 官方地址取得 IPv4/IPv6 范围，校验内容与 Nginx 配置，通过后重载，失败时回滚。成功后再把 `api`、`console` 改成橙云；`gw` 仍为灰云。Nginx 只接受这些可信 CF 来源提供的真实地址，再把清洗后的单个地址传给应用；应用的两个 `*_TRUSTED_PROXY_IPS` 仍是本机实际回源地址，`TRUST_CF_CONNECTING_IP` 仍为空。

重新执行普通域名访问和第 7 节的源站 `--resolve` 检查，确认 HTTPS、root 登录、设备授权、配置事件和真实客户端 IP 均正常。若已有 Worker 通配路由覆盖新子域，先核实其行为；不要为此次接入删除现有 Worker，存在干扰时继续保持灰云。

Cloudflare 橙云是代理层,并不会自动禁止直接访问源站。因为 gw 必须公开同一源站的 443,不能把整机 443 一律限制为 CF 来源;将来如果依赖 Cloudflare Access 保护控制台,需要对 console 的 Nginx server 块另设来源或回源认证边界。完成后继续执行 §10 的隐藏源站检查单。

### 9.1 境外访问：备案优先，隧道只做过渡

**先处理备案**：拦截页已明确提示“网站暂时无法访问 / 去腾讯云备案控制台”。正确处理顺序：

1. **在腾讯云备案控制台确认 `faction168.online` 的状态**：未备案则提交备案；已在他处备案则办理**接入备案**；也可以换用已在腾讯云接入备案的域名。备案通过前，裸域名指向大陆服务器会被持续/间歇拦截。
2. **备案完成前**，境外访问可以临时用命名 Cloudflare Tunnel 做验证/过渡。隧道由服务器主动出向建立，能绕开入向 443 的 TLS 重置，但**不要把它当作规避备案的长期方案**；它不改变域名在大陆的备案合规要求。
3. **若不想或不能备案**：把公开入口迁到香港/新加坡等境外地域（注意国内访问质量），或使用境外域名；不要继续把未备案域名解析到大陆服务器。
4. **备案完成后再做境外加速**：优先 EdgeOne/GAAP/合规 CDN，保留国内三域灰云直连。

**命名隧道（临时/过渡或已备案后的境外加速）**：

- 在 Cloudflare Zero Trust 创建命名隧道，建议使用独立子域 `console-global`/`api-global`/`gw-global`，保留国内三域灰云直连，避免国内用户绕道境外。
- 服务地址填 `https://127.0.0.1:443`；`Origin Server Name` 和 `HTTP Host Header` 都填对应的国内域名（console/api/gw）。这样 Nginx 的 SNI 路由、`CONSOLE_ALLOWED_HOSTS` 和同源 CSRF 校验都能通过，且源站证书仍按 Let's Encrypt 正常校验，不需要关闭 TLS 验证。
- 仓库示例：`deploy/cloudflare/tunnel-config.example.yml`。
- DNS：`cloudflared tunnel route dns <TUNNEL_ID> console-global.faction168.online`（或在控制台创建 CNAME 到 `<TUNNEL_ID>.cfargotunnel.com`），使用橙云。
- 进程管理：`cloudflared service install <TOKEN>`，或把示例配置放到 `/etc/cloudflared/config.yml` 后 `systemctl enable --now cloudflared`。
- 隧道传输优先 `http2`；本机实测 QUIC/TCP 预检都通过，http2 更容易穿过防火墙。

**2026-10-19 验证结果**：

- 直连三域：本机对 `/console/`、`/healthz` 等非根路径的 HTTP 请求 10/10 被 302 到腾讯云备案拦截页；check-host 12 个境外节点中 11 个在 TLS 握手阶段被 RST；Jina 也报 `ERR_CONNECTION_RESET`。
- 服务器本地/回环 HTTPS 10/10 正常，Nginx 无握手错误；根路径偶尔返回本站点的 308，说明拦截不是服务器配置能修复的。
- 临时 quick tunnel：check-host 10 个节点中 8 个返回 HTTP 200，控制台页面、API（未登录 401）和静态资源均可达；静态资源仍是 gzip 242KB（原始 810KB）。
- quick tunnel 无 uptime 保证，只能做验证；且不能替代备案合规。

**个人梯子**：

- 把 `console/api/gw.faction168.online` 和源站 IP 加进代理客户端的 **DIRECT 规则**，可以避免“国内→境外→上海”的绕路；但它**不能绕过腾讯云的备案拦截**。拦截来自域名/边缘策略，必须先备案或换域名/境外主机。
- 不要在全局代理模式下测试国内站点速度：国内→境外→上海会绕一圈，反而更慢。

**边界**：

- `gw` 仍建议保持国内灰云直连；Cloudflare 代理对长流式响应有 100 秒 idle/524 风险，首 token 很慢的模型可能被截断。若必须全球加速网关，优先 EdgeOne/GAAP 并实测长流。
- 独立 `*-global` 域名下，应用生成的绝对链接（设备授权等）仍使用 `PUBLIC_APP_ORIGIN=https://console.faction168.online`；境外用户可能被带到国内域名。上线前实测设备授权/登录跳转；若不可接受，需要把 `PUBLIC_APP_ORIGIN` 指向全球域名，或用 EdgeOne/GAAP 在同一域名上做全球加速。
- 4Mbps 出向是云厂商上限；gzip/BBR 改善有效吞吐和丢包恢复，不会突破上限。
- 备案拦截同样会截断 HTTP-01 续期；备案完成前改用 DNS-01，避免 2027-01-17 证书到期后无法续期。

官方参考:[Cloudflare IP 列表](https://www.cloudflare.com/ips/)、[Full (strict) 模式](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)、[长请求与 524 超时](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-524/)。

## 故障处理和回退

- **证书申请失败**：保持 HTTP 引导配置，检查三条 A、错误 AAAA、80 入站和 challenge 文件直出；不安装引用缺失证书的 TLS 配置。若 challenge 被 302 到 `dnspod.qcloud.com/static/webblock.html`，是备案拦截，先备案或改用 DNS-01，调 Nginx 无效。
- **HTTPS 308 循环/POST 403**：确认 CF Full (strict)，Nginx 保留 Host 并设置 `X-Forwarded-Proto $scheme`，不要从客户端照抄该头。
- **境外 443 TLS 被 RST、80 正常**：先检查 HTTP 是否被 302 到 `dnspod.qcloud.com/static/webblock.html?d=<域名>`；若是，属于腾讯云备案/接入拦截，按 §9.1 先备案或换域名/境外主机，不要反复改 Nginx。隧道/专线只能做过渡或境外加速。
- **静态资源没有 gzip**：确认 `shucode-gzip.conf` 已安装、console/api server 块已 include、`gzip_proxied any` 生效；gw 必须保持 `gzip off`。
- **BBR 未生效**：`sysctl net.ipv4.tcp_congestion_control` 应为 `bbr`，`tcp_bbr` 模块已加载；已有网卡的 qdisc 可能到重启后才变为 fq，BBR 本身不依赖。
- **网关流式异常**：若客户端在 HTTP/2 下流式中断，先把 gw server 块的 `http2 on;` 去掉（console/api 保留），reload 后复测；不要改代理超时来掩盖。
- **性能配置回退**：现场备份在 `/root/shucode-perf-backup-*`（站点配置、proxy-common 和原 default 站点目标）。恢复文件、按需重新链接 `/etc/nginx/sites-enabled/default`，`nginx -t` 后 reload；删除 `/etc/sysctl.d/99-shucode-network.conf` 和 `/etc/modules-load.d/tcp_bbr.conf` 后执行 `sysctl --system`。
- **root 登录成功但设备页仍要登录**：检查 `PUBLIC_APP_ORIGIN` 是否为 console，设备页是否同域；不要先扩大 cookie 域来掩盖配置错误。
- **流式首字延迟或约 60 秒中断**：检查实际生效 location 的 buffering、gzip 和 `proxy_read_timeout`，以及网关/供应商自己的 header、idle 超时；保持 gw 灰云。
- **所有用户显示同一个来源 IP**：重新实测容器 TCP 对端与可信代理精确地址；橙云时确认已先安装官方 CF ranges，并由 Nginx 清洗 XFF。

需要回退时，从第 3 节的备份恢复本次修改过的 `.env` 和对应 Nginx 站点/snippets，再 `nginx -t` 后 reload，并使用相同镜像标签仅重建 api/gateway。不要覆盖备份后其他人新建的站点；若恢复后的公开地址已失效，应先恢复可用的 DNS/入口或保留灰云 HTTPS 入口。回退不删除数据库卷、不重新创建 root、不重跑种子，也不将应用端口重新暴露公网。

## 10. 隐藏源站 IP 检查单

源站 IP 的隐藏只能靠「代理层 + 边缘加固 + 面上无泄漏」一起工作，源站自身配置无法单方面做到。2026-10-19 起仓库文档已用 `<源站公网IP>` 占位，不再出现真实 IP；以下是执行清单。

### 10.1 原则与边界

- **必须有代理层**:DNS A 记录指向的真实地址就是能被解析出来的地址。api/console 改 Cloudflare 橙云(§9)后，公网 DNS 只解析到 Cloudflare;源站自身无法仅通过配置"隐藏"。
- **隐藏 ≠ 隔离**：橙云隐藏的是 DNS 记录；扫描整个 IP 段 / 证书内容 / 历史快照仍可能反查。金标准是**源站 IP 轮换**:一旦 IP 泄入公开仓库、历史快照或扫描数据库(证书透明度、Shodan 类)，"隐藏"就失效，换新 IP + 全站橙云才是干净的起点。
- **gw 仍保持灰云**(§9 的既定取舍):网关必须公开 same-IP 443,所以该源站对针对性扫描依然可达。两条路线：(a) 接受现状,靠 12.2 的默认站点拒绝降低扫段指纹;(b) gw 也橙云(CF 对长流式 SSE 的代理需实测)或为 gw 单独分配源站 IP——收益是 api/console 公司接口入口完全 CF 化。

### 10.2 仓库已内置的边缘加固

`deploy/nginx/faction168.online.conf`（与引导配置 `faction168.online-http.conf`）已加入**默认站点拒绝**：

```server 块行为
HTTP  default_server（无 Host/陌生 Host 直连）→ 444 立即断开，无响应
HTTPS default_server（无 SNI/陌生 SNI）      → ssl_reject_handshake，TLS 握手失败,不做证书协商
```

- ACME 续期按 `Host` 命中正式站点，不受 default_server 影响。
- 宿主机还承载其他站点时，如已有其他 `default_server` 声明，`nginx -t` 会报错；届时二选一保留，不要同时声明。
- 安装正常按 §6 执行，`nginx -t` 通过后 reload。

### 10.3 DNS 面审计（每次改 DNS 后必做）

所有指向 `<源站公网IP>` 的记录都构成泄漏源，改橙云或改指向：

```bash
for name in api console gw www mail smtp imap web panel tests; do
  dig +short A "$name.faction168.online" @1.1.1.1
done
dig +short MX faction168.online @1.1.1.1
dig +short TXT faction168.online @1.1.1.1
```

- 实测记录（2026-10-19）：`MX` 走 Spaceship 邮件转发（`mx1/mx2.efwd.spaceship.net`），不指向源站；`www` **灰云直指源站 IP**，必须改橙云或改指向其他服务。
- 其他业务系统（面板、图床、旧站点）不要与这三域名共用同一源站 IP,否则任何一条记录都可以反查源站。
- 域名本身的保密价值有限（对外本来就是公开服务)，重点是 IP:任何灰云记录都是 IP 泄漏面。

### 10.4 代理层开着之后的 DNS 与行为基线

- `dig +short api.faction168.online` / `console.faction168.online` 返回 Cloudflare 段(104.x/172.x/188.x 开头)，不再是源站 IP。
- 直连源站验证:
  ```bash
  curl -fsS https://<源站公网IP>/                 # TLS 握手失败（默认站点拒绝）
  curl -sS -m 5 -o /dev/null -w '%{http_code}' http://<源站公网IP>/  # 000（连接被断开）
  ```
  注意应通过 `systemctl status nginx` 关注 access_log——默认 444/握手拒绝也会留一条日志，缺失时可确认归因。
- 域名正常访问的行为基线见 §7;橙云后的 `client_ip` 也应仍是真实来源(§7.5)。

### 10.5 别的常见泄漏面

- **证书透明度日志**：Let's Encrypt 证书会被 CT 记录 SAN（api/console/gw 域名，不含 IP）。暂无威胁,但说明"从证书查域名"是公开操作，域名侧防御要做足。
- **HTTP `Location` 跳转**：80 → 308 的 Location 带域名,不带 IP;保持 308 覆盖面足够。
- **应用错误页**（应出于源站为 8787/8788 的绑回环，不会在报错中出 IP）。
- **免密 prose**：任何 SSH 是密码口令登录的做法、`.env` 中的真实 IP、git commit 里的 IP 一律不再入库;`.shucodeignore` 的 `docs/`、`specs/`、`design/` 本地命中只影响索引类工具,不影响 git 跟踪。
- **git 历史**:若 IP 曾入过已推送的历史提交，HEAD 脱敏不等于历史不载;仓库公开时会增补"历史保留旧 IP"的风险。彻底清除需 `git filter-repo` + force push,或者走 IP 轮换更干净。
