# 独立模板服务直连与统一面板

客户端直接向 `https://templates-api.azurpilot.com/api/v3/post/templates/unknown` 上传图标，批量入口 `/api/v3/post/unknown-candidates`；公开限流接口不需要共享服务令牌。模板、图标和映射资源直接从 `https://templates.azurpilot.com/latest.json` 开始下载。

统一面板仍在主服务器 `/admin/`。模板页直接跨域调用 `https://templates-api.azurpilot.com/internal/v1/templates` 及其 `/status`、`/mappings/{U-ID}`、`/publish`、`/releases`，沿用已有登录凭据。模板服务器 `TEMPLATE_ADMIN_TOKEN` 设置为同一个 `ADMIN_TOKEN`，配置 `TEMPLATE_ADMIN_ORIGINS` 精确来源白名单。浏览器不调用主服务器模板代理，也不把 Cloudflare 或事件凭据发送到前端。

主服务器只保留以下配置与接口：

```dotenv
TEMPLATE_EVENT_TOKEN=<与模板服务相同>
V3_ENABLED=true
```

`POST /internal/v1/templates/events` 接收已发布映射变化和状态，不接收图标、模板清单或资源包。`release.published.payload.replace_ids` 只包含本次批准的映射变化，累计映射保存在 Worker。主服务器以事务保存映射、同步版本及幂等回执，原始统计保留 U-ID，重算受影响历史小时，返回精确 ack。事件重试最大间隔 256 秒。

主服务器没有模板上传客户端、资源代理、管理代理或模板版本回源逻辑。下列新旧入口均已移除并返回 404：

- `/api/v3/post/templates/unknown` 和 `/api/v3/post/unknown-candidates`。
- `/api/v3/get/templates/manifest`、`/api/v3/get/templates/delta`、`/api/v3/get/mappings`。
- `/api/v1/admin/template-service/*`。
- `/api/v1/admin/template-candidates`、`/api/v1/admin/template-candidates/{id}`、`/api/v1/admin/templates`、`/api/v1/admin/template-releases`。
- `/api/v1/admin/templates/publish`、`/api/v1/admin/templates/rollback`、`/api/v1/admin/templates/test`。
- `/internal/v1/template-candidates`、`/internal/v1/template-releases/{id}`、`/internal/v1/worker/sync`。

主服务器不再使用 `TEMPLATE_ADMIN_URL`、`TEMPLATE_ADMIN_TOKEN`、`TEMPLATE_INGEST_URL`、`TEMPLATE_INGEST_TOKEN`、`TEMPLATE_PUBLIC_BASE`、`TEMPLATE_PROXY_CACHE`。

验证覆盖直连上传、批量去重、CORS、同一管理员凭据、限流、模板路由不读取正文、无图片事件、增量映射与历史重算。WebUI 仅构建和验证请求路由，不做样式测试。

旧 `cmd/templateserver` 是历史独立实现，不是当前部署入口。当前服务在独立 AzurPilotCloud 仓库，不与主服务共享进程或数据库。
