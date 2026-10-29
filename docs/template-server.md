# 模板服务器与未知模板闭环

> 旧实现参考文档。当前独立模板服务由 AzurPilotCloud 仓库部署，管理页面接入主服务器同一个 `/admin/` 面板；当前接入配置与行为请看[独立模板服务接入](template-service-integration.md)。下文的独立模板后台不再是新服务的部署方式。

模板服务器是独立部署的二进制（`cmd/templateserver`），拥有自己的 PostgreSQL，负责：

1. 模板的增删改查与去重匹配；
2. 客户端未知模板上传的感知哈希聚类、匹配与注册；
3. Ed25519 签名、manifest/delta/mappings 生成；
4. 通过 Wrangler 把静态产物部署到 Cloudflare Worker（绑定自定义域名）。

主 API 只负责 v3 委托/科研统计，并作为客户端入口转发未知模板上传、代理模板读取。

```
客户端 ──POST /api/v3/post/templates/unknown──► 主 API ──Bearer TEMPLATE_INGEST_TOKEN──► 模板服务器
客户端 ──GET  /api/v3/get/templates/*──────────► 主 API ──缓存/代理──► CF Worker / 模板服务器
管理员 ──/api/v1/templates/*───────────────────► 模板服务器（ADMIN_TOKEN）
```

## 域名分工

| 域名 | 指向 | 用途 |
|---|---|---|
| `templates-api.azurpilot.com` | 模板服务器 `:8090`（经 CDN 回源） | 管理台 `/admin/`、管理 API、主 API 转发的未知模板上传 |
| `templates.azurpilot.com` | Cloudflare Worker | 客户端读取 manifest/delta/mappings 与图标 |

两者都是必需或可替代的：管理面走 `templates-api`，分发面走 Worker；Worker 未就绪时主 API 的模板代理可临时指回 `templates-api`（模板服务器自身提供同样的公开读取路径）。

## 域名硬约束

Worker **必须**绑定 `templates.azurpilot.com`（或其它 `azurpilot.com` 子域）自定义域名：

- `*.workers.dev` 在该网络下 DNS 被污染且 TLS 被 SNI 重置；
- 自定义域名的证书由 Cloudflare 自动签发；
- Cloudflare API Token 需要 Workers Scripts Edit、Workers Routes Edit、DNS Edit、Zone Read。

## 配置

主 API（`cmd/server`）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `V3_ENABLED` | `true` | 关闭后 v3 上报/查询返回 503 |
| `AGG_INTERVAL` | `1h` | 小时聚合间隔，最小 1m |
| `TEMPLATE_PUBLIC_BASE` | `https://templates.azurpilot.com` | manifest/delta/mappings 上游 |
| `TEMPLATE_PROXY_CACHE` | `60s` | 模板读取缓存 |
| `TEMPLATE_INGEST_URL` | 空 | 未知上传转发目标；空时上传返回 503 |
| `TEMPLATE_INGEST_TOKEN` | 空 | 与模板服务器一致 |

模板服务器：

| 变量 | 默认 | 说明 |
|---|---|---|
| `LISTEN_ADDR` | `:8090` | 监听地址 |
| `DATABASE_URL` | 本地 postgres | 独立数据库 |
| `ADMIN_TOKEN` | 空 | 管理 API；空则管理接口 503 |
| `TEMPLATE_INGEST_TOKEN` | 空 | 客户端上传转发凭据；空则关闭上传 |
| `TEMPLATE_SIGNING_KEY` | 空 | base64/hex 的 32 字节 seed 或 64 字节私钥；空则启动生成临时密钥 |
| `TEMPLATE_PUBLISH_DIR` | `templates-public` | 本地发布目录 |
| `TEMPLATE_PUBLIC_BASE` | `https://templates.azurpilot.com` | Worker 自定义域名 |
| `CF_DEPLOY` | `false` | true 时执行 Wrangler 部署 |
| `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_API_TOKEN` | 空 | `CF_DEPLOY=true` 时必填 |
| `CF_WORKER_NAME` | `azurpilot-templates` | Worker 名称 |
| `TEMPLATE_WRANGLER_IMAGE` | `azurpilot-wrangler:4` | 非空时用 `docker run <image> wrangler deploy`；空时直接调用 PATH 中的 wrangler |
| `TEMPLATE_SELF_CHECK` | `true` | 部署后轮询 manifest 的 `X-Manifest-SHA256` |
| `TEMPLATE_EXPIRES_DAYS` | `30` | manifest 过期时间 |

`deploy/template-compose.yml` 使用内置 Node + Wrangler 的镜像，并把 `TEMPLATE_WRANGLER_IMAGE` 置空，因此不需要挂载 Docker socket。

## 管理台（WebUI）

模板服务器在 `/admin/` 提供内嵌的 React + Ant Design 管理台（源码在 `template-web/`，构建产物嵌入二进制）：

- 登录：输入 `ADMIN_TOKEN`，令牌存于浏览器 localStorage，随请求以 Bearer 发送；令牌错误或过期会退回登录页。
- 模板：搜索/筛选、上传（自动缩放为 49×49 PNG）、编辑元数据、**修改模板 ID**、启用/禁用。
- 未知模板：按状态查看聚类、映射（可同时注册为模板）、合并、拒绝。
- 发布记录：revision、部署状态、manifest 哈希与 Worker 地址。
- 委托目录：粘贴 `commission_names.json` 导入。
- 任何修改都会标记「有修改未发布」，点右上角「发布分发」生成新 revision；发布成功才推送到 Cloudflare 与本地镜像。

页面本身不含凭据，所有操作走下方需要 `Authorization` 的管理 API；`ADMIN_TOKEN` 为空时管理台可打开但接口返回 503。

## 管理 API

除公开读取接口外均需 `Authorization: Bearer <ADMIN_TOKEN>`。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/status` | 当前 revision、启用模板数、待处理聚类、分发地址 |
| `GET` | `/api/v1/templates` | 列表，支持 `enabled/scene/item/limit` |
| `POST` | `/api/v1/templates` | 新建/更新；body `{key?,name,item,tag,scene,kind,image_base64,allow_duplicate?}` |
| `PATCH` | `/api/v1/templates/{key}` | 只改元数据/启停：`{name?,item?,tag?,scene?,kind?,enabled?}` |
| `POST` | `/api/v1/templates/{key}/rename` | 修改模板 ID：`{new_key}`；目标已存在返回 409 |
| `DELETE` | `/api/v1/templates/{key}` | 禁用（下一次发布进入墓碑） |
| `POST` | `/api/v1/templates/publish` | 生成 revision、签名并部署；body `{note}` |
| `POST` | `/api/v1/templates/import` | 导入 `commission_catalog` |
| `GET` | `/api/v1/templates/releases` | 发布历史与部署状态 |
| `GET` | `/api/v1/templates/unknown` | 未知聚类列表，支持 `status/scene/limit` |
| `POST` | `/api/v1/templates/unknown/{key}/map` | 映射并（默认）注册为模板 |
| `POST` | `/api/v1/templates/unknown/{key}/reject` | 拒绝聚类 |
| `POST` | `/api/v1/templates/unknown/merge` | 合并聚类 `{from_key,to_key}` |

### 模板上传的匹配与去重

- `image_base64` 必须是正方形 PNG；管理上传固定 49×49，未知上传 8–512 像素。
- 服务端计算内容 SHA-256；未提供 `key` 时自动生成 `T-<sha256 前 16 位>`。
- 若已有启用模板使用相同图标且 `key` 不同，返回 `409 TEMPLATE_DUPLICATE`，并在 `details.existing_key` 指出已有模板；确需重复时传 `allow_duplicate: true`。
- 写入后立即使感知哈希缓存失效。

## 未知模板上传与闭环

### 客户端协议

主 API：`POST /api/v3/post/templates/unknown`

```json
{
  "scene": "commission",
  "image_base64": "<正方形 PNG>",
  "sha256": "<可选，必须与图片一致>",
  "client": "azurpilot/7.2.0"
}
```

请求体 ≤512 KiB，图片 ≤384 KiB。响应：

```json
{"schema_version":3,"status":"registered","cluster_key":"U-0123456789abcdef","occurrences":1,"revision":318}
```

`status`：

| 值 | 含义 |
|---|---|
| `matched` | 命中已注册/已知模板，返回 `template_key/item/tag` |
| `registered` | 首次登记为新聚类 |
| `pending` | 命中待处理聚类 |
| `mapped` | 命中已映射聚类，返回 `item/tag` |
| `rejected` | 该图标此前被管理员拒绝 |

### 匹配算法

1. 对 PNG 做 9×8 灰度采样，计算 64 位 dHash（透明像素按白色合成）。
2. **已知模板匹配**：启用模板按场景过滤后取汉明距离最小者；要求距离 ≤8，且比次佳候选至少好 2 位，避免边界样本误命中。命中即返回 `matched`，不创建聚类。
3. **聚类匹配**：在未拒绝的未知聚类中同样取最近者；命中则计数 +1、刷新 `last_seen_at`；命中已合并聚类时沿 `unknown_cluster_aliases` 解析到目标。
4. **注册**：未命中时以 `U-<dHash 十六进制>` 为 `cluster_key` 新建聚类，保存图标、SHA-256、场景与客户端版本。

同一图标的不同上传、并发上传都由 `cluster_key` 主键与行锁保证只建一行、只累加计数。

### 人工确认与分发

- `map` 写入 `unknown_mappings`（revision = 当前版本 +1），更新聚类状态。
- `register=true`（默认）时同时把聚类图标注册为 `kind=temporary` 的模板；模板 key 即 `cluster_key`。
- 合并时把计数转移给目标、禁用源模板、记录别名；已有源映射会迁移到目标。
- 拒绝会禁用同名模板，之后同图标上传只返回 `rejected`。
- **映射与注册在下一次 `POST /api/v1/templates/publish` 后对客户端生效**：mappings 进入 `mappings.json`，注册模板进入 manifest/delta。

## 发布产物

`writeDist` 生成：

```
manifest.json        模板清单（含 image_base64）
manifest.sig         Ed25519 对 manifest.json 原始字节的签名（base64）
manifest.sha256      manifest 的 SHA-256
mappings.json        聚类 → 物品映射与别名
icons/<sha256>.png   49×49 或聚类原图
deltas/0.json        全量
deltas/<from>.json   相对最近 50 个 revision 的增量 + 墓碑
```

发布流程在 `template_state` 单例行锁内分配 revision；部署或自检失败时保留 `deployed=false` 记录，`current_revision` 不前移，客户端继续读取旧版本。

Worker 路由：

- `GET /api/v3/get/templates/manifest` → manifest + 签名头
- `GET /api/v3/get/templates/delta?from=N` → 增量，缺失回退 `deltas/0.json`
- `GET /api/v3/get/mappings` → mappings
- 其余路径走静态资源，支持 CORS 与 `OPTIONS`

## 部署

构建包含管理台的二进制或镜像（`cmd/templateserver` 通过 `go:embed` 内嵌 `template-web` 产物，缺少产物时用 `-tags noadminui` 构建）：

```bash
npm --prefix template-web ci && npm --prefix template-web run build   # 输出到 internal/templateserver/webui/dist
make template-server                                                  # 或 docker build -f deploy/template-server.Dockerfile .
```

1. 构建 Wrangler 镜像（可选，仅 docker 模式需要）：
   ```bash
   docker build -t azurpilot-wrangler:4 deploy/template-worker
   ```
2. 在模板服务器上：
   ```bash
   cp deploy/template.env.example deploy/template.env
   # 填写数据库密码、ADMIN_TOKEN、TEMPLATE_INGEST_TOKEN、TEMPLATE_SIGNING_KEY、
   # CF 凭据，并把 CF_DEPLOY 设为 true
   docker compose -f deploy/template-compose.yml --env-file deploy/template.env up -d --build
   ```
3. 在 Cloudflare 控制台或首次部署时确认 `templates.azurpilot.com` 已创建（Worker 自定义域）。
4. 主 API 配置 `TEMPLATE_INGEST_URL=http://<模板服务器>:8090/api/v3/post/templates/unknown` 与相同的 `TEMPLATE_INGEST_TOKEN`。
5. 从模板服务器与主服务器分别验证：
   ```bash
   curl -sS https://templates.azurpilot.com/api/v3/get/templates/manifest | head -c 200
   curl -sS -D- -o /dev/null https://templates.azurpilot.com/api/v3/get/templates/manifest
   ```

本地开发可设 `CF_DEPLOY=false`：发布只写 `TEMPLATE_PUBLISH_DIR`，模板服务器自身提供相同的公开读取路径。

## 安全与隐私

- 上报事件不包含截图、账号 ID、UID；未知上传只保存裁剪后的模板小图与客户端版本。
- 管理 Token 与上传 Token 分离；主 API 不向客户端暴露任何 Token。
- 模板图标只接受 PNG，先校验尺寸再解码，避免解压炸弹；文件按内容哈希命名，拒绝路径穿越。
- 凭据只放在环境变量或未提交的 `deploy/template.env` 中。
