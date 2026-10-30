# ap-cloud-server（AzurPilot 云 API v2 服务端）

自建云 API 的 Go 实现，对应《自建云 API 重构与统一请求规范》（重构计划）中的自建服务部分：

- **GET /api/v2/get/server** —— 单服状态（实时查询游戏网关，带缓存）
- **GET /api/v2/get/maintenance** —— 区服维护安排
- **POST /api/v2/post/cl1** —— CL1 月度快照（去重、冲突检测、指标计算）
- **科研与通用掉落统计** —— 三节点截图流水线的结果接收、版本订正、汇总查询和模板管理，见[部署与接口文档](docs/statistics-pipeline.md)。支持委托名称／收益／成功状态及未知物品归类，升级见[委托与签名模板协议](docs/commission-templates.md)。A/B 位于项目目录的独立仓库 `AzurPilotUploadServer`、`AzurPilotStatsWorker`。
- **AP 客户端兼容** —— multipart 图片、月度遥测及掉落记录查询，见[兼容矩阵与统计口径](docs/legacy-api.md)。
- 公告与更新策略静态文件的**生成与 Cloudflare Workers 发布**（凭据只保存在服务器端）
- **/api/v1/admin/*** —— 运维管理接口（Bearer Token，非客户端契约）

技术栈：Go 1.26 标准库 `net/http` + `pgx/v5`（PostgreSQL），无 Web 框架、无 ORM。

## 目录结构

```
cmd/server/            入口：装配、信号处理、优雅停机
internal/config/       环境变量配置
internal/gateway/      游戏网关协议层（TCP protobuf 10018/10019 + HTTP JSON，移植自 AzurLaneServerStatus）
internal/statuscache/  区服状态缓存（TTL + single-flight + 后台保活）
internal/store/        PostgreSQL 访问（内嵌迁移、CL1 事务、维护、公告、发布历史）
internal/httpapi/      HTTP 层（v2 动态接口、统一错误信封、限流、管理接口）
internal/publish/      静态文件渲染 + Cloudflare Direct Upload 发布器（按 Worker 串行）
internal/statistics/   识别结果、科研/掉落汇总、模板候选与 B 节点控制协议
migrations -> internal/store/migrations/*.sql（go:embed）
workers/               两个静态 Worker 的 wrangler 模板（初始创建与本地调试用）
public/                本地静态发布目录（Worker Static Assets 内容）
```

## 快速开始

原生 Go 启动前须显式加载环境变量；程序不自动读取 `.env`。Docker Compose 会读取项目配置文件。

```bash
# 本地开发（需本机 PostgreSQL）
cp .env.example .env   # 按需修改
go run ./cmd/server

# Docker Compose（server + postgres）
docker compose pull
docker compose up -d

# 测试（无 PG 时自动跳过集成用例）
go test ./...

# 完整集成测试
TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/apcloud_test?sslmode=disable" go test -count=1 ./...
```

Docker 镜像为 `ghcr.io/wess09/azurpillotcloudserver:latest`，内含管理后台，支持 Linux amd64/arm64。主分支发布 `latest / main / sha-<完整提交号>`；版本标签由 Release 工作流发布。Compose 的 `STATISTICS_IMAGE` 可固定版本；开发时使用 `docker compose up -d --build`。数据库 `pgdata` 和静态文件 `publish` 使用独立持久化卷。

## API 契约

动态接口统一要求公共请求头（计划第 1 节），`X-AzurPilot-ID` 必须为 32 位十六进制：

```http
User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) ... Chrome/131.0.0.0 Safari/537.36
X-AzurPilot-Ture: true
X-AzurPilot-ID: <32位十六进制设备ID>
Cloudflare-Workers-Version-Key: <与设备ID相同>
```

### GET /api/v2/get/server?region=cn&server_id=28

- `region` 仅允许 `cn / cn_ios / cn_channel / en / jp / tw`；`server_id` 为正整数。
- 状态枚举：`normal / full / reg_full / maintenance / unopened / unknown`。
- 参数错误 400；服务器不存在 404 `SERVER_NOT_FOUND`；网关不可用且无缓存 503 `SOURCE_UNAVAILABLE`。
- 网关数据缓存 8 秒（TTL 可配），后台每 5 秒保活刷新；刷新失败时复用旧数据（即使已过期）并记录错误码。

### GET /api/v2/get/maintenance?server=cn

- `server` 仅允许 `cn / en / jp / tw`。
- 有安排返回 `maintenance` 对象（时间均为带时区 RFC 3339），无安排返回 `maintenance: null`。
- 数据源（数据库）失败返回 503 `SERVICE_UNAVAILABLE`，不用 null 冒充成功。

### POST /api/v2/post/cl1

- `Content-Type: application/json`（否则 415），正文 ≤ 64 KiB（否则 413）。
- 严格校验：未知字段拒绝、布尔/浮点冒充整数拒绝、`device_id` 必须与请求头一致、
  `request_id` 必须 UUID、`month` 必须有效 `YYYY-MM`、`collected_at` 必须带时区、
  `battle_count` ≥ 1（零战斗不提交）。
- 云端计算指标：`battle_rounds = battle_count // 2`，`sortie_cost = battle_rounds × 120`，
  `akashi_probability = round(encounters/rounds, 4)`，`average_stamina = round(ap/encounters, 2)`
  （与 Python `round` 语义一致，除数为零时结果为 0）。
- 写入规则（单事务原子提交）：
  - 按 `(device_id, instance_id, month)` 覆盖更新，不累加；
  - 同 `request_id` 同正文重试 → 原业务结果 + `duplicate: true`（去重保留 24 小时）；
  - 同 `request_id` 不同正文 → 409 `REQUEST_CONFLICT`；
  - 旧快照不覆盖新快照 → 409 `STALE_SNAPSHOT`。

### 统一错误信封（计划 3.6）

```json
{"schema_version": 2, "error": {"code": "...", "message": "...", "details": {}}}
```

### 管理接口（运维用，非客户端契约）

需要 `Authorization: Bearer <ADMIN_TOKEN>`；`ADMIN_TOKEN` 未配置时整体关闭。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/PUT/DELETE | `/api/v1/admin/announcement` | 公告管理；PUT/DELETE 触发公告 Worker 发布 |
| GET/PUT | `/api/v1/admin/update-policy` | 强制更新开关；PUT 触发更新 Worker 发布 |
| GET | `/api/v1/admin/maintenance` | 全部维护计划 |
| PUT/DELETE | `/api/v1/admin/maintenance/{server}` | 设置/删除区服维护计划 |
| GET | `/api/v1/admin/publish/status` | 每个 Worker 最近一次发布状态 |

### 管理后台 Web UI（React）

服务端内嵌一个 React 管理后台，挂在 **`/admin`**（如 `http://127.0.0.1:8080/admin/`），
与二进制一同分发，无需单独部署前端：

- **Token 登录**：输入服务端 `ADMIN_TOKEN`，凭据只保存在浏览器 localStorage；
- **公告管理**：发布（自动触发公告 Worker 发布）、撤回（发布 `announcement: null`）；
- **更新策略**：`force_update` 开关，保存即发布；
- **维护计划**：国服启动时及每 10 分钟同步[官方维护公告](https://game.bilibili.com/blhx/news/)，后台可立即同步、查看原文和失败诊断；人工覆盖优先，删除覆盖恢复官方计划。其他区服暂由人工录入；
- **发布状态**：每个 Worker 最近一次发布结果（成功 / 仅本地 / 失败 + 错误），30 秒自动刷新；
- 401 自动回到登录页；服务端未配置 `ADMIN_TOKEN` 时给出明确提示。

开发与构建（React 18 + Vite + TypeScript，无 UI 框架依赖）：

```bash
cd web
npm install
npm run dev      # 开发模式：http://localhost:5173，/api 代理到 127.0.0.1:8080
npm run build    # 产物输出到 internal/adminui/dist，随后 go build 完成嵌入
```

或一键构建完整二进制：`make all`。CI 中前端单独构建（类型检查 + 打包），
产物下载后参与五平台交叉编译；Docker 镜像包含独立的 Node 构建阶段。
前端未构建时服务仍可编译运行（`-tags noadminui` 可显式排除），访问 `/admin` 会得到构建指引占位页。

**设计规范**：UI 遵循 [DESIGN.md](DESIGN.md)（[awesome-design-md](https://github.com/VoltAgent/awesome-design-md)
格式，Cloudflare Dashboard 设计语言：浅色画布、白卡片细边框、橙色仅用于 Logo、操作蓝用于全部交互、
软色语义徽章）。调整 UI 前先读该文件；新增页面按其中的组件规范实现。

## Cloudflare 发布

两个静态 Worker（`ap-notice`、`ap-update-policy`）分别以
`public/api/v2/get/notice`、`public/api/v2/get/update` 为 Static Assets 目录，
按完整 URL 路径提供 `data.json`（`workers/` 内含 wrangler 模板用于初始建站与路由绑定）。

服务器端发布器（权威通道，凭据只保存在服务器端）流程：

1. 依数据库状态渲染完整 JSON 文档（撤回 = 发布 `announcement: null`，不删文件）；
2. 原子写入本地发布目录；
3. Cloudflare Direct Upload REST API：注册 manifest → 上传变更文件 → 完成令牌原子部署；
4. 从公网地址（`CF_*_PUBLIC_URL`）读取确认内容一致后才记录发布成功；
5. 每 Worker 严格串行；任一步失败保留上一版本并记录 `publish_history`。

配置 `CF_ACCOUNT_ID` + `CF_API_TOKEN` 启用；留空时只生成本地文件（开发模式）。

静态缓存：Workers Static Assets 自带 ETag 与条件请求处理（客户端 `If-None-Match` 命中返回 304），
默认 `must-revalidate` 保证内容更新即时生效——正好满足计划中"静态缓存继续使用 ETag 和 Cache-Control"的要求。

## 协程安全（无泄露）

- 所有后台协程（缓存保活、发布队列、限流器回收、数据清理）由 `context` 取消 + `WaitGroup`
  回收，停机顺序：HTTP 优雅停机 → 取消 ctx → 等待全部协程 → 关闭连接池；
- 缓存 single-flight 的刷新协程同样纳入 WaitGroup，`Close` 会等待在途查询（受超时约束）；
- 每个请求有处理超时；全部出站调用（网关、Cloudflare、读取确认）都带 deadline；
- 限流器映射表带空闲回收，避免长期运行下的内存泄露；
- 测试套件使用 `go.uber.org/goleak` 在每个包的 `TestMain` 中强制检查协程泄露。

## CI/CD（多平台）

- **ci.yml**：golangci-lint；三平台测试矩阵（ubuntu/windows/macos）+ 竞态检测 +
  goleak（ubuntu 附带 PostgreSQL service 跑集成用例）；五平台交叉编译
  （linux/amd64、linux/arm64、windows/amd64、darwin/amd64、darwin/arm64）；
  docker buildx 构建 linux/amd64 + linux/arm64 镜像（push 到 main 时发布 GHCR）。
- **release.yml**（推送 `v*` 标签）：回归测试后构建五平台二进制（tar.gz/zip）上传 GitHub Release，
  多平台镜像以 semver 标签发布到 GHCR。

## 配置

全部通过环境变量（见 [.env.example](.env.example)），关键项：

| 变量 | 默认 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP 监听地址 |
| `DATABASE_URL` | 本机 PostgreSQL | libpq 连接串 |
| `ADMIN_TOKEN` | 空（关闭） | 管理接口 Bearer Token |
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | `5000` / `8000` | 每个来源 IP、请求类别的每秒请求数 / 突发请求数 |
| `CACHE_TTL` / `CACHE_REFRESH_INTERVAL` | `8s` / `5s` | 网关缓存 |
| `DEDUP_TTL` | `24h` | CL1 去重保留 |
| `CF_ACCOUNT_ID` / `CF_API_TOKEN` | 空 | Cloudflare 发布凭据 |
| `PUBLISH_DIR` | `public` | 本地静态发布目录 |

公开接口、管理接口（含 `/admin/` 资源）和 `/internal/` Worker 接口各自使用独立额度，
`GET/HEAD /healthz` 不参与限流。5000/8000 表示每秒补充 5000 个请求令牌、最多积累 8000 个，
不代表同时执行 8000 个数据库查询。来源 IP 使用直连地址；反代共用地址时，同类请求共享额度，
不自动信任客户端提供的 `X-Forwarded-For`。超限仍返回 429 和 `Retry-After`，并记录访问日志。
