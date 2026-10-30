# AzurPilot 主 API

独立 Go / PostgreSQL 后端，负责区服状态、维护安排、CL1 月度快照、公告与更新策略发布，以及委托和科研的结构化统计。截图上传、A/B 识别流水线和嵌入式前端已移除。

## 部署边界

| 入口 | 职责 | 托管 |
|---|---|---|
| `api.azurpilot.com` | 主业务 API、管理 WebSocket、映射事件接收 | 主服务器 |
| `templates-api.azurpilot.com` | 图标直传、编号、匹配、模板管理 WebSocket | 独立 NAT 模板服务器 |
| `templates.azurpilot.com` | 签名模板、增量、映射和图标静态分发 | Cloudflare Worker |
| `admin.azurpilot.com` | 统一管理主服务及模板服务 | Cloudflare Pages |
| `stats.azurpilot.com` | 公开委托/科研统计页面 | Cloudflare Pages |

前端源代码：[新建独立仓库 AzurPilotCloudWeb](https://github.com/wess09/AzurPilotCloudWeb)。两个 Pages 项目直接连接这个 GitHub 仓库，推送 `main` 自动构建。主机器不构建、不提供也不保存项目的前端资源。

管理浏览器分别直连两个服务的 WebSocket。模板图像不会经过主服务器。模板服务器仅通过 `POST /internal/v1/templates/events` 向主服务推送映射变化和发布状况。

## 数据库

全部数据库操作使用 GORM；表、键和索引通过模型声明。核心模型在 `internal/store/models.go`，统计模型在 `internal/v3db/`。业务条件使用类型化表达式，聚合使用 ORM 库提供的表达式；没有手写数据库语句或 SQL 迁移文件。`internal/database/no_raw_sql_test.go` 保护此约定。

两个领域共用一个连接池，最多 16 个连接。`orm_schema_versions` 记录各领域版本；迁移在事务和行锁保护下按顺序执行。后续扩展追加版本迁移，不改写已执行的步骤。旧 A/B 和旧内置模板表通过 ORM Migrator 定向移除，活跃业务表继续使用。

主库活跃表：`cl1_snapshots`、`cl1_dedup`、`maintenance_schedules`、`maintenance_sources`、`announcements`、`update_policy`、`publish_history`、`drop_events`、`commission_items`、`research_items`、`commission_hourly`、`commission_item_hourly`、`research_hourly`、`research_item_hourly`、`aggregation_dirty_hours`、`commission_catalog`、`template_sync_state`、`template_item_mappings`、`template_event_receipts`、`orm_schema_versions`。

原始统计保留 U-ID 与数量。正式映射发布后，仅重算受影响小时，汇总使用正式 ID；不丢弃历史记录。

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| GET / HEAD | `/healthz` | 健康检查 |
| GET | `/api/v2/get/server` | 区服状态 |
| GET | `/api/v2/get/maintenance` | 维护安排 |
| POST | `/api/v2/post/cl1` | CL1 月度快照 |
| POST | `/api/v3/post/drops` | 结构化委托/科研上报，最多 200 条、1 MiB |
| GET | `/api/v3/get/commission` | 委托汇总 |
| GET | `/api/v3/get/commission/options` | 委托筛选选项 |
| GET | `/api/v3/get/research` | 科研汇总 |
| GET | `/api/v3/get/research/options` | 科研筛选选项 |
| WS | `/api/v1/admin/ws` | 主服务管理 |
| POST | `/internal/v1/templates/events` | 服务间映射/状态事件，独立事件凭据 |

v2 动态接口要求 `X-AzurPilot-ID`。v3 查询支持 `from/to/server`；委托附加 `comm/status`，科研附加 `series/project/item/tag`。时间范围最多 366 天，默认 30 天；委托公开展示要求样本数大于 50。公开统计 CORS 仅允许配置的 `PUBLIC_ORIGINS`，不使用管理凭据。

兼容接口保留：`POST /api/telemetry`、`GET /api/get/announcement`、`GET /api/force_update`、`GET /api/v1/servers/{id}`、`GET /api/maintenance`。公告和更新策略的静态资源继续由独立公告/策略 Worker 分发。

`/`、`/admin`、`/admin/`、旧 v2 掉落/科研查询及 time-slots、所有截图上传/分片接口、统计预览/重处理、A/B 内部接口和 REST 管理接口均不再注册，返回 404。无上传重定向或代理。

管理协议和方法见 [WebSocket 协议](docs/admin-websocket.md)，模板边界见 [独立模板接入](docs/template-service-integration.md)。

## 本地与验证

```powershell
Copy-Item .env.example .env
# 将 .env 配置加载到当前进程环境后启动
go run ./cmd/server
go vet ./...
go test -count=1 ./...
```

`TEST_DATABASE_URL`、`TEMPLATE_TEST_DATABASE_URL` 指向隔离 PostgreSQL 后运行数据库用例。Linux CI 执行竞态检测及泄露检查。`make build` 只构建后端，Docker 最终镜像为 distroless 非 root 镜像，无前端构建阶段。

配置参考 [.env.example](.env.example)。生产主服务入口 `127.0.0.1:18090` 反向代理到容器 8080；代理必须支持 WebSocket Upgrade。管理凭据只存于服务端配置和登录浏览器。数据库及 Cloudflare 密钥不写入代码或构建产物。
