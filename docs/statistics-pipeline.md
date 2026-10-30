# 结构化统计链路

旧截图 A/B 上传和识别流水线已经废弃，主服务不再接收截图、识别结果、分片上传、预览、重处理或 Worker 命令。对应路由、配置、业务代码和旧表均已移除。

客户端完成本地识别后向 `POST /api/v3/post/drops` 上报结构化委托/科研记录，每批最多200条、1 MiB。`record_id` 全局幂等。未知图标直接上传独立模板服务得到 U-ID，不通过主服务器。

原始记录保存到 `drop_events` 及 commission/research items；每小时汇总到统计表。映射发布事件触发受影响历史小时重算。公开 GET 查询仅返回允许展示的物品，委托门槛为大于50个样本。

公开页面在 `stats.azurpilot.com`；管理页面在 `admin.azurpilot.com`，管理操作使用 WebSocket。参阅 [当前接口清单](../README.md) 和 [管理协议](admin-websocket.md)。
