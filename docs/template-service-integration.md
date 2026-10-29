# 独立模板服务与统一管理面板

当前模板服务由独立仓库 **AzurPilotCloud** 部署，拥有自己的 Python 进程、PostgreSQL、签名密钥和 Worker 发布配置。主服务器不负责模板匹配与编号，也不直接访问模板数据库。

## 配置

| 主服务器环境变量 | 含义 |
|---|---|
| `TEMPLATE_ADMIN_URL` | 独立模板服务 HTTPS 基址 |
| `TEMPLATE_ADMIN_TOKEN` | 服务器端管理代理令牌，与模板服务一致 |
| `TEMPLATE_INGEST_URL` | 模板服务 `/api/v3/post/templates/unknown` 的完整地址 |
| `TEMPLATE_INGEST_TOKEN` | 图标上传转发令牌，与模板服务一致 |
| `TEMPLATE_EVENT_TOKEN` | 模板事件令牌，与模板服务一致 |
| `TEMPLATE_PUBLIC_BASE` | Cloudflare Worker 的资源分发基址 |

模板服务器配置 `MAIN_EVENT_URL=https://<主服务器>/internal/v1/templates/events`。生产使用 HTTPS；本机联调允许 loopback HTTP。

管理员继续使用已有 `ADMIN_TOKEN` 登录 `/admin/`，进入“模板服务”。页面展示未知编号、映射草稿、模板导入、发布和推送状态，无第二次登录；服务令牌保留在主服务器，浏览器只调用 `/api/v1/admin/template-service/*`。

## 数据流

- 客户端未知图标经过主服务器转发，由模板服务匹配并返回正式 ID 或全局 U-ID。
- 模板库下载地址由上传响应提供，客户端直接向 Cloudflare Worker 下载签名模板和历史编号映射。
- 模板服务推送注册、发布成功和失败事件；主服务器以事务保存事件回执及已发布映射，返回精确 `ack`。
- 事件重复不重复生效，相同 ID 的不同正文被拒绝；旧版本不会覆盖新映射。
- 统计原始行保留 U-ID，聚合时使用 `template_item_mappings` 解析。新映射标记受影响的历史小时，并即时唤醒聚合，不要求重新上报原始事件。
- 主服务器上报响应的模板版本从本地同步状态读取，避免为了取版本每次回源模板服务。

该接入依赖现有 `V3_ENABLED=true`；关闭时模板服务保留未确认事件等待恢复。

## 验证

```powershell
$env:TEMPLATE_TEST_DATABASE_URL='postgres://user:password@localhost:5432/dedicated_test_database'
go test -count=1 ./internal/templatesync ./internal/httpapi ./internal/drops
npm --prefix web run build
```

数据库用例只对专门的测试数据库运行，验证映射发布、重复事件、旧版本拒绝、51 条样本闭环的底层历史修复行为。完整跨服务与 Worker 验证脚本位于 AzurPilotCloud 的 `tools/verify_local.py`。

旧 `cmd/templateserver` 和 `template-web` 只是旧实现，不是当前部署入口；旧文档 `template-server.md` 描述的独立后台和共享模型不适用于这里。
