# 保留的兼容接口

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/api/telemetry` | 旧 CL1 遥测适配至核心快照事务 |
| GET | `/api/get/announcement` | 公告状态 |
| GET | `/api/force_update` | 强制更新策略 |
| GET | `/api/v1/servers/{id}` | 旧区服状态协议 |
| GET | `/api/maintenance` | 旧维护计划协议 |

旧截图上传 `/api/upload` 及全部 v1/v2 分片上传已删除，返回404，不重定向。旧 v2 科研/掉落结果和 time-slots 已删除。新统计使用 v3 结构化上报和查询。

CL1 按设备、实例和月份覆盖快照，同请求ID同正文幂等、异正文冲突。较旧时间不能覆盖新快照；兼容上报另保留累计值不倒退保护。查询和写入均通过 ORM，未保留原 pgx 直写实现。
