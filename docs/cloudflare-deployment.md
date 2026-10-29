# Cloudflare 入口配置

`api.azurpilot.com` 和 `stats.azurpilot.com` 使用黄云直接回源 C 的 Nginx，Nginx 由服务器管理员维护；B 的 `recognizer.azurpilot.com` 使用独立 Tunnel。

## DNS 与 TLS

| 主机 | 类型 | 源站地址 | 代理 |
|---|---|---|---|
| `api`、`stats` | A | `43.133.246.197` | 开启 |
| `api`、`stats` | AAAA | `240d:c000:f05f:8d00:444:e532:9610:0` | 开启 |

两个主机不再使用 `cfargotunnel.com` CNAME。黄云开启时，公网 DNS 查询返回 Cloudflare 边缘地址；上表地址是 Cloudflare 控制台中的源站值。

Configuration Rule 仅匹配这两个主机，设置 `ssl: strict`，保留其他站点原有 TLS 模式。源站须提供覆盖对应域名的有效公共证书或 Cloudflare Origin CA 证书，并支持 IPv4/IPv6 的 443 端口。Go 服务仍映射在 `127.0.0.1:18090`，PostgreSQL 不发布公网端口。

## Worker 路由

新旧主机均保留以下静态发布路由：

| 路径模式 | Worker |
|---|---|
| `/api/v2/get/notice/*` | `ap-notice` |
| `/api/v2/get/update/*` | `ap-update-policy` |

其余请求由 C 处理。A 的 `STATISTICS_PUBLIC_URL`、B 的 `STATISTICS_URL`、C 的两个 Worker 发布回读 URL 均使用 `api.azurpilot.com`。

模板 Worker 使用独立的自定义域名 `templates.azurpilot.com`，不使用 `*.workers.dev`（该网络下被 SNI 阻断）。发布流程、签名与 unknown 模板闭环见 [template-server.md](template-server.md)。

## 验证与交接

切换前确认源站证书、SNI 和双栈监听；切换后检查 `/healthz`、`/admin/`、区服/维护/统计查询及两个 Worker JSON。确认 B 仍在线，结果上报和回执没有积压。

2026-10-27 切换时，曾新增 C 上 `/www/server/panel/vhost/nginx/azurpilot-api.conf` 并通过语法检查、重载；其后反代配置由服务器管理员接管。后续自动化只维护 Cloudflare 配置，不修改 Nginx。保留 C 的服务和数据卷，B 的 Tunnel 与 A/B 反向连接保持独立。
