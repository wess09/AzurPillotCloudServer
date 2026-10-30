# 三节点统计系统

## 仓库和职责

项目目录下三个独立 Git 仓库分别为：

| 节点 | 仓库 | 持久化内容 |
|---|---|---|
| A，上传入口 | `AzurPilotUploadServer` | SQLite 哈希/文件/通知记录，未上传完的暂存图 |
| B，本地识别 | `AzurPilotStatsWorker` | SQLite 任务、结构化结果、模板版本、诊断 |
| C，当前仓库 | `AzurPillotCloudServer` | PostgreSQL 结果历史、当前统计贡献、候选小图、模板和管理状态 |

123 盘保存长期原图。A 云盘上传完成即删除本地副本；B 在处理结束/失败后删除工作图片，重试重新下载；C 不保存完整截图。A 每 30 分钟派发，模板重跑和试识别不受此调度限制。

AP 先请求 `https://api.azurpilot.com/api/v1/uploads/init`，C 返回 307 指向 A 的公网入口。AP 随后直接向 A 登记、上传文件和完成图片组；图片正文不经过 C。A 与 B 通过现有 WebSocket 交换任务，B 将识别结果提交 C，因此 A、C 不需要彼此连通。

新 AP 默认使用 `/api/v2/uploads/init`，同样由 C 307 到 A。v2 只通过 A 登记元数据和申请预签名地址，图片由 AP 直接上传 123 盘；A 向云盘核验 MD5、大小和目标目录后才进入既有半小时派发队列。C 同时支持 v2 的 `files/{index}/prepare`、`files/{index}/complete` 和组 `complete` 重定向，仍不访问 A 或读取请求正文。v1 和历史上传入口继续支持旧客户端；B/C 的结果格式与采集时间不变。完整契约见 [A 的 v2 上传协议](https://github.com/wess09/AzurPilotUploadServer#上传协议)。

## 配置与部署顺序

对外 API 统一使用 `https://api.azurpilot.com`，管理后台继续使用 `https://stats.azurpilot.com/admin/`。A 的 `STATISTICS_PUBLIC_URL` 和 B 的 `STATISTICS_URL` 指向统一 API 域名。新旧域名共享 C 服务和数据库；B 的预览入口仍由 `RECOGNIZER_URL` 单独配置。

C 的两个域名通过黄云 A/AAAA 直接回源公网 Nginx；反代由服务器管理员维护，Cloudflare 配置见 [入口部署记录](cloudflare-deployment.md)。B 继续使用自己的 Tunnel。

三个服务均提供 GHCR 镜像，不需要在服务器上编译；各仓库复制 `.env.example` 为 `.env` 并配置后，执行 `docker compose pull` 和 `docker compose up -d`：

| 节点 | 镜像 |
|---|---|
| A | `ghcr.io/wess09/azurpilotuploadserver:latest` |
| B | `ghcr.io/wess09/azurpilotstatsworker:latest` |
| C | `ghcr.io/wess09/azurpillotcloudserver:latest` |

`main` 跟踪主分支，`sha-<完整提交号>` 固定内容；正式版本可使用版本标签。更新时重新拉取镜像并重建容器，保留数据卷。B 已包含固定客户端源码、模板和 CPU OCR 模型；Docker 部署不需宿主机 Alas/Python。只有原生运行 B 时才需要下面的 `ALAS_ROOT / ALAS_PYTHON` 配置。

1. C 按现有 Docker Compose 启动 PostgreSQL 和服务，配置 `ADMIN_TOKEN` 与独立的 `STATISTICS_TOKEN`。新 SQL 迁移随服务启动执行；升级前备份数据库。
2. B 按其 README 配置 `STATISTICS_URL`、相同 `STATISTICS_TOKEN`、123 盘开放平台凭据和可用的 `ALAS_ROOT`/`ALAS_PYTHON`。设置 `UPLOADER_WS_URL` 主动连接 A，应用层使用 `WORKER_TOKEN` 鉴权。A 只有 HTTP/NAT 入口时，通过 [SSH 本地转发](https://github.com/wess09/AzurPilotStatsWorker/blob/main/deploy/reverse-ssh.md) 加密此连接；无需新增公网映射。B 的 Cloudflare Tunnel 仍用于 C 的预览接口。
3. A 配置相同 `WORKER_TOKEN`、`WORKER_MODE=reverse`、`BATCH_INTERVAL=30m` 和 123 盘凭据，启动两路上传进程。经过 Cloudflare Tunnel 时，客户端使用 `POST /api/v1/uploads/init` 登记完整元数据，逐文件 `PUT /api/v1/uploads/{upload_id}/files/{index}`，最后 `POST /api/v1/uploads/{upload_id}/complete`。每个图片请求最多 10 MiB，整组仍可达 500 MiB；具体重试协议见 [A 的上传文档](https://github.com/wess09/AzurPilotUploadServer#上传协议)。兼容的整组 multipart 请求需要允许至少 502 MiB 的 HTTPS 入口，不能直接经过免费套餐的 100 MB 请求上限。
4. C 若要按需查看原始帧，配置 `RECOGNIZER_URL=https://B域名`、`WORKER_TOKEN`。若 B 还经过 Cloudflare Access，必须让该服务请求通过相应 Access 策略。
5. 打开 `/admin/` 的“识别概览”“待分类模板”“模板库”，确认 B 在线。分类保存后会自动发布并重识别；首页 `/` 提供无需登录的公开统计网站。

单文件和单组大小限制可通过 `MAX_FILE_BYTES / MAX_GROUP_BYTES` 调整；三个节点应使用相同设置，默认分别为 10 MiB 和 500 MiB。A 的接收并发、云盘上传并发和暂存配额独立配置。

真实 123 盘、域名、Tunnel 和密钥由部署环境提供。不要将它们写入仓库。123 盘开放平台下载额度必须覆盖 B 的识别、重试和预览流量。

## A → B 协议 v1

B 主动建立到 A 的受认证 WebSocket，A 通过同一连接发送以下消息。每小时 :00/:30，将完整上传的唯一组登记为固定待发队列；1 份也发送，空时段不发。单条消息最多 30 份，较大时段拆为多条，末条允许不足 30 份。消息最大 16 MiB，不含原图。

```json
{
  "type": "batch_ready",
  "protocol_version": 1,
  "batch_id": "0123456789abcdef0123456789abcdef",
  "window_start": "2026-10-26T11:30:00Z",
  "window_end": "2026-10-26T12:00:00Z",
  "items": [{
    "sha256": "图片组的64位小写哈希",
    "metadata": {
      "upload_id": "303d1f40-3280-4053-84dd-574a83f59d20",
      "device_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "instance_id": "bbbbbbbbbbbbbbbb",
      "project": "research",
      "scene": "research_complete",
      "server": "cn",
      "collected_at": "2026-10-26T12:00:00+08:00",
      "file_sizes": [12345]
    },
    "files": [{"index": 0, "size": 12345, "sha256": "文件的64位小写哈希", "file_id": 123}]
  }]
}
```

组哈希是各文件 SHA-256 **二进制 32 字节摘要按索引拼接**后的 SHA-256。重排文件会改变组哈希，元数据不参与哈希；全局相同组只保留首次提交归属。B 验证索引、大小、组摘要并事务持久化后回复：

```json
{"type":"batch_accepted","batch_id":"0123456789abcdef0123456789abcdef"}
```

ACK 丢失时 B 重连，A 补发原批次 ID；B 对同 ID 不同正文拒绝接收，对相同批次重复确认。云盘文件确认完成前，组不能成为可派发样本。连接每 25 秒保活，B 在断开后每 5 秒重连；重启恢复队列及调度时间。兼容模式可让 A 设置 `WORKER_MODE=forward` 主动连接旧的 B 接口。

## B → C 结果与回执

内部 API 需要 `Authorization: Bearer <STATISTICS_TOKEN>`，JSON 请求上限 8 MiB。`POST /internal/v1/statistics/results` 每次接受 1–100 个结果：

```json
{
  "results": [{
    "sha256": "与source相同的组哈希",
    "revision": 1,
    "status": "complete",
    "parser_version": "azurpilot-adapter-v1",
    "template_release": 1,
    "source": "这里使用上一节items中的完整对象",
    "events": [{"event_type":"complete","series":9,"project":"Q-268-MI","focus_series":0,"duration_seconds":3600,"items":[{"item":"Coins","amount":100,"tag":""}]}],
    "diagnostics": [], "candidate_ids": [], "used_templates": []
  }]
}
```

上例哈希和 `source` 为说明占位，实际必须提供合法哈希及对象。科研事件为 `refresh`（无掉落）或 `complete`，通用掉落为 `drop`。刷新每个出现项目是一条事件。状态为 `complete / needs_review / invalid`；只有 `complete` 参与正式统计。

HTTP 200 返回逐项 `receipts`，状态 `accepted / duplicate / conflict / invalid / retry`。B 仅对前两者完成任务，`retry` 保留结果重传，冲突/无效进入失败待查。`GET /internal/v1/statistics/results/{sha256}` 查询当前 `revision` 和状态，404 表示尚无记录。

C 按组哈希事务串行：初版从 1 开始，更正必须是当前版本 +1；同版本同内容幂等，同版本不同内容冲突。新版本在同一事务中删除旧统计贡献、写入新贡献，同时保留每版原始结构化结果。更正变为待复核时也会撤下旧贡献。CL1 原有接口和存储独立。

## 统计查询

公开网站为 `https://stats.azurpilot.com/`，提供科研项目分布、48 个半小时时段出现占比和来源掉落明细。保留原有页签按钮、筛选栏、下拉选择、场景输入框及表格样式，通过“应用筛选”查询。科研默认显示“全部期数”和“全部倾向（含未识别）”，也可在原有下拉框中指定期数、倾向。选择全部时，同名项目按期数、倾向分别列行，半小时图也使用对比项目对应期数、倾向的独立分母。科研项目表保留原有列；指定刷新倾向时，出现次数和占比采用该倾向，完成记录保留原有按项目期数汇总的口径；完成奖励在掉落明细中按倾向分别列行。通用掉落场景留空时显示全部来源，也可输入 `commission`、`opsi_daily` 及其他来源进行筛选，各来源的掉落率和单次均量仍分别计算；候选项来自实际数据。筛选和页面链接可保存当前期数、倾向或来源。公开页面只调用公开查询 API，不读取管理 Token。管理后台的“公开统计网站”链接可直接进入。

- `GET /api/v2/get/research`
- `GET /api/v2/get/drop`

可选参数 `from / to` 为带时区 RFC3339，区间 `[from,to)`，默认最近 30 天，最大 366 天；只提供 `to` 时从该时刻回溯 30 天。支持 `server / scene / series / focus_series / project / timezone` 筛选；`focus_series=unknown` 可单独查询倾向未识别（存储值 0）的记录，省略则返回各倾向的独立分组。返回 `groups / items / daily / timezone`；每天按 `timezone` 分组，汇总接口默认 `UTC` 以保留原有契约，公开页面显式使用 `Asia/Shanghai`。查询返回当前有效版本，月度查询使用自然月起止时间，不会把历月重复累加。

`GET /api/v2/get/research/options` 和 `/api/v2/get/drop/options` 返回实际有效数据的筛选维度：`options` 每行包含 `series / focus_series / scene / events / appearances / samples`。仅使用 `from / to / server` 限定发现范围，已选的期数、倾向、场景、项目不裁掉其他选项。只有正式统计事件提供选项，不硬编码科研期数或场景范围；空筛选保留“全部”的含义，不自动改成最新期数、倾向或某个掉落来源。

`GET /api/v2/get/research/time-slots`（通用掉落对应 `/api/v2/get/drop/time-slots`）按一天内的 48 个半小时时段汇总，默认 `timezone=Asia/Shanghai`，支持 IANA 时区和同样的筛选。返回 `slots`，含 `slot_index / slot_start / series / focus_series / project / scene / appearances / samples / total_appearances / appearance_rate`。科研期内出现占比 = 当前项目在该时段的出现次数 ÷ 该时段相同期数、相同科研倾向且符合区服、场景筛选的全部出现次数；即使不指定期数或倾向，分母也分别计算。筛选项目只裁剪返回行，不缩小分母。空时段省略，不代表出现概率为零。通用掉落不产生科研刷新观测，其 `appearance_rate` 无科研出现率意义，掉落率应使用汇总接口的 `items.drop_rate`。

统计统一依据原始 `collected_at`；派发时段、识别完成时间、迟到上报和模板重跑不能改变样本归属时段。`window_start/window_end` 仅标识传输批次的派发时段。科研出现记录与掉落样本的分离参考 [统计局科研项目解析](https://github.com/LmeSzinc/azur-stats-server/blob/3a743f091b0a98d42a8dc04c3d16be9552a9c7d6/AzurStats/scene/research_projects.py) 和 [科研掉落统计](https://github.com/LmeSzinc/azur-stats-server/blob/3a743f091b0a98d42a8dc04c3d16be9552a9c7d6/AzurStats/stats/research_items.py)，半小时调度与分时 API 是本系统扩展。

`groups.appearance_rate` 为相同期数、相同倾向内的项目槽位占比，实际分母通过 `total_appearances` 返回，不受项目筛选影响；这不是项目在完整五槽位列表中至少出现一次的概率。`groups` 和 `items` 均保留 `focus_series`，同名项目跨期数、跨倾向分别统计。物品 `amount` 是总数量，`occurrences` 是出现该物品的完成事件数，`samples` 包含未掉该物品的完成事件，`drop_rate=occurrences/samples`，`average_per_sample=amount/samples`。掉落按场景、项目、期数、倾向和标签独立统计，委托样本不会进入大世界每日的分母。`amount_per_hour` 仅计算有时长的事件；无可用时长返回 null。物品标识来自模板名称，显示名称、物品所属期数与科研项目期数分别存储。

原有科研维度查询的变化不需要重识别；委托事件和未知物品的新协议需要 [委托与模板升级说明](commission-templates.md) 所述的 `0004` 迁移。前端依赖 `/options` 接口，部署时应与后端一起更新。依赖旧版跨期合并占比的 API 消费方，应读取期数、倾向分组；原有默认 UTC 日汇总保持兼容。

新增有效记录会累加到相同维度的统计中；新的期数和 `scene` 来源会自动成为可选项，未知来源名暂按原始标识展示。这里的自动发现指现有字段的新取值，新增 JSON 字段或未支持的奖励标签仍需更新协议。选择“全部区服”时按筛选意图合并区服；海域、侵蚀等级等仍保留在原始结果及旧版逐条查询中，本页面未增加这些维度的细分统计。项目、物品、时段及选项各自最多返回 100000 条汇总分组，超过时返回 `QUERY_TOO_BROAD`；该上限不限制原始样本数。正常的跨期数、跨倾向查询完整返回全部分组，页面继续按每页 25 条展示掉落明细，不会静默截断数据。

## 模板闭环与管理 API

B 自动提取未知图标，小图内容 SHA-256 为候选 ID；相同来源/文件帧/槽位重传不增加次数。候选 PNG 为 8–256 像素、最多 128 KiB；目前 B 提取 49×49 内部图标。C 只保存小图，人工确认名称和类别，支持同物品多个外观。

| 管理操作 | API（均需 ADMIN_TOKEN） |
|---|---|
| 识别概览、最近结果 | `GET /api/v1/admin/statistics/overview`、`/statistics/results` |
| 候选、模板、版本、指令 | `GET /api/v1/admin/template-candidates`、`/templates`、`/template-releases`、`/worker-commands` |
| 分类或忽略 | `PATCH /api/v1/admin/template-candidates/{id}` |
| 发布、回滚 | `POST /api/v1/admin/templates/publish`、`/templates/rollback` |
| 草稿试识别、重跑 | `POST /api/v1/admin/templates/test`、`/statistics/reprocess`，正文 `{"ids":["样本或候选哈希"]}` |
| 一键重试缺模板上报 | `POST /api/v1/admin/statistics/reprocess-unresolved`，无需正文；仅重试确实缺模板的来源 |
| 原始帧流式预览 | `GET /api/v1/admin/statistics/preview/{sha256}/{index}?frame=0` |

分类正文为 `{"status":"assigned","template":{"item_id":"Coins","name":"物资","category":"consumable","rarity":0,"series":0,"scene":"all","enabled":true}}`；忽略用 `status=ignored`。类别为 `ship_blueprint / equipment_blueprint / consumable / other`；模板场景 `all` 可跨科研和通用掉落使用，也可限制为 `research / drop`。

分类保存会在同一事务中更新模板、冻结完整模板包和 SHA-256、切换活动版本，返回 `release_id / queued_samples`，无需再手动发布。新增或修改启用的识别模板时，只自动重跑当前缺模板的来源；模板修改、禁用或回滚时，只订正使用过受影响模板的结果。显示名称等元数据修改、重复发布相同识别内容不生成重跑任务。待分类页的“一键重试缺模板”也只重试缺模板来源，合并同版本待处理指令。

项目、数量、奖励画面或其他纯解析失败只保留失败历史，不进入模板自动重试流程、不计入正式统计。后台默认展示“等待补充模板”，其他失败可在“解析失败历史”中查看；识别器修复后可通过单条 `/statistics/reprocess` 明确重试。内部状态 `needs_review` 保留用于协议兼容，界面不再表示需要人工复核。

回滚复制旧内容生成新版本，保持版本号递增；当前模板编辑值不随回滚改动。B 通过 `POST /internal/v1/worker/sync` 每 30 秒上报心跳/指令进度并获取活动版本和指令，通过 `GET /internal/v1/template-releases/{id}` 下载并验包。重处理不等待 30 分钟上传调度；每个识别任务固定模板快照，试识别不写正式结果。

识别概览的 `counts` 按唯一上报计数，`image_counts` 按上报包含的原图数量计数；一次上报可以含多张图。这两个数字只覆盖 B 已回传的结果，B 本地排队任务另行显示。正式统计中的项目槽位和完成事件也不是图片张数。

## 验证

C：`TEST_DATABASE_URL=<专用PG> go test -race -count=1 ./...`、`go vet ./...`、`npm --prefix web run build`。A/B 各自 README 提供单测和启动命令。可选 B→C 联调在 B 仓库运行 `tests/test_c_integration.py`，需要全新专用 C 数据库、`STATISTICS_TEST_URL / STATISTICS_TEST_TOKEN / STATISTICS_TEST_ADMIN` 及 Alas 环境；使用真实 OCR 与真实 C/PostgreSQL，仅替换云盘下载。

实际 123 盘与 Tunnel 需填写部署凭据后另行验收；本地测试不会自动调用真实云盘或公开服务端口。

A 仓库还提供可选 `TestRealWorkerWebSocketContract`，通过专用 B 实例验证 30 份上传后的真实 WebSocket 派发、鉴权和持久化确认；云盘上传仍使用磁盘校验替身。
