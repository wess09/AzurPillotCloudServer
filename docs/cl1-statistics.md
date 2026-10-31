# 侵蚀 1 公开统计

`GET /api/v3/get/cl1` 返回 `schema_version: 3` 与 `result`，不需要设备或管理凭据。`result.total` 为累计汇总，`result.history` 为按月份倒序排列的汇总，`result.updated_at` 为最新快照更新时间，无数据时为 null。

数据来自现有 `cl1_snapshots`：每个设备、实例、月份只保存一份累计快照。重复提交更新原快照，不累加上报次数。公开响应不包含设备数量、设备编号或实例编号，汇总查询不读取设备编号。

每个范围先合计所有实例的 `battle_count`、`akashi_encounters`、`akashi_ap`，再统一计算：

| 字段 | 计算 |
| --- | --- |
| `battle_count` | 战斗总场数 |
| `battle_rounds` | 战斗总场数 ÷ 2，向上取整；在汇总后取整 |
| `akashi_encounters` | 明石遭遇总次数 |
| `akashi_ap` | 明石商店购买的行动力总数 |
| `ap_cost` | 出击总轮数 × 5 |
| `net_ap` | 购买行动力 − 消耗行动力 |
| `akashi_probability` | 明石遭遇总次数 ÷ 出击总轮数 |
| `average_ap` | 购买行动力总数 ÷ 明石遭遇总次数 |
| `cycle_efficiency` | 净行动力 ÷ 消耗行动力 |

累计和每月范围分别汇总原始计数，再计算各自的轮数与比率；不平均实例或月份的比率，不采用存储中旧的 `sortie_cost` 计算结果。分母为零时 API 返回 0，页面显示“—”。月份使用客户端上报的月份，本月选择按北京时间确定。没有迁入旧站历史数据，也不生成补齐月份。
