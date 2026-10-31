# 统一产物分析

`GET /api/v3/get/analysis` 为公开科研与军事委托提供统一统计。前端迁移至 Next.js 后使用该接口；旧 `/commission`、`/research` 查询保留兼容。

## 参数

| 参数 | 含义 |
| --- | --- |
| `scene` | `research` 或 `commission`，必填 |
| `target` | `category:blueprint-rainbow` 等分类，或 `item:BlueprintAgir` 等具体物品，必填 |
| `from` / `to` | RFC3339，左闭右开，默认最近 30 天，最多 366 天 |
| `server` | 已支持区服，空值为全部 |
| `series` | 科研期数 |
| `category` | 科研字母类别或 Lua 委托类型；未知类型为 `unknown` |
| `duration` | 单任务配置时长，秒 |
| `group_by` | `project`（默认）、`category`、`duration`；科研还支持 `series` |
| `timezone` | 沿用 v3 时区校验；记录范围统一转换为 UTC |

分类及单项 ID 来自嵌入的 `internal/drops/analysis_catalog.json` 与模板服务已发布的物品定义，同 ID 以发布定义为准。`GET /api/v3/get/items` 返回合并后的目录，包括分类、稀有度、科研期数、所属版本、单位、统计场景、游戏 ID、阵营与舰种或装备类型。不接受客户端自定义混合单位的目标集合。不存在的目标、区服、场景或分组返回 400。接口使用现有公开统计 CORS 来源配置，不要求管理凭据。

## 返回与定义

响应信封包含 `schema_version: 3`、`from`、`to`、`scene`、`target` 与 `result`。`result` 和每个 `result.groups` 都包含如下可合并的原始量：

- `samples`：全部匹配完成记录，包含零掉落。
- `occurrences`：目标总量大于零的完成记录数；同一条记录出现多个物品或标签只计一次。
- `amount`、`min`、`max`：先合并每条记录的匹配物品，再汇总与求极值。
- `total_seconds`：各样本对应任务配置时长之和。
- `unknown_durations`：没有可靠任务时长的样本数。
- `extra_amount`：科研 `bonus` 标签产出，已包含在总量中。
- `other_amount`：`catchup`、`event` 标签产出，已包含在总量中。
- `success_samples` / `success_amount`：大成功委托样本数及这些记录的目标产出，已包含在总量中，不代表额外增量。

派生值为 `drop_rate = occurrences / samples`、`average = amount / samples`、`per_hour = amount × 3600 / total_seconds`。任一未知时长样本存在时，该分组的 `per_hour` 为 null。汇总重新累加原始量，不平均现有比率。委托分组沿用超过 50 样本才公开的门槛；总览只汇总已公开的分组。

每个项目分组还给出 `id`、`name`、`series`、`category`、`duration`、`costs` 及 `condition`。合并分组的项目专属资料留空，不能把第一个项目的时长或消耗用于整个分组。

## 实现与目录更新

使用只读 REPEATABLE READ 事务读取原始事件及当前已发布映射；按 record_id 分页，每批最多 1000 条，只读取该批关联奖励，避免加载完整时间范围的明细。映射在内存副本上解析，原始奖励不变。所有数据库操作均使用 GORM 模型和 typed clause，不包含手写 SQL。

任务资料来自本地 AzurLaneLuaScripts 国服 Lua，物品名称/图标变体及多语言委托别名来自客户端仓库。目录记录来源 SHA-256；同名昼夜委托优先使用客户端明确代码。翻译别名歧义被剔除；无法确定的任务不猜测时长。数据库中已经配置的委托时长优先于静态目录。

在 `AzurPilotCloudWeb` 中运行 `tools/generate-catalog.py --client ../AzurLaneAutoScript --lua ../AzurLaneLuaScripts --backend ../AzurPillotCloudServer`，同时更新两端目录，再分别验证并提交。部署顺序为主服务 → 前端，避免新前端先调用不存在的接口。

测试覆盖分类联合命中、零掉落、单项隔离、当前映射、不同任务时长的加权重分组、昼夜识别、超过 1000 条记录的批次读取、大成功产出及未知时长。设置 `TEMPLATE_TEST_DATABASE_URL` 指向隔离 PostgreSQL 后执行 `go test ./internal/drops -run TestAnalysis`。
