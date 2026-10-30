# 管理 WebSocket 协议

统一入口 `https://admin.azurpilot.com`。浏览器分别连接：

- 主服务：`wss://api.azurpilot.com/api/v1/admin/ws`
- 模板服务：`wss://templates-api.azurpilot.com/internal/v1/templates/ws`

两个服务共用管理员凭据，连接后五秒内发送 `{"type":"auth","token":"管理员凭据"}`，成功返回 `{"type":"authenticated"}`。凭据不放入 URL，不通过主服务器代理模板请求。Origin 必须精确匹配各自白名单。

命令：`{"id":"唯一请求编号","method":"方法名","params":{}}`。

成功：`{"type":"result","id":"相同编号","data":{}}`。

失败：`{"type":"error","id":"相同编号","error":{"status":422,"code":"VALIDATION_ERROR","message":"参数不合法"}}`。

订阅使用 `state.subscribe`；初次响应含完整状态，变化时推送 `{"type":"event","topic":"state","data":{}}`。前端自动重连并恢复订阅，断线时拒绝尚未完成的操作，不自动重放写命令。重新连接后核对服务端状态。

| 主服务方法 | 参数 |
|---|---|
| `state.get` / `state.subscribe` / `ping` | 空对象 |
| `announcement.get` | 空对象 |
| `announcement.publish` | `title`，`content` 或 `url` 至少一项 |
| `announcement.withdraw` | 空对象 |
| `update_policy.get` | 空对象 |
| `update_policy.set` | `force_update` 布尔值 |
| `maintenance.list` / `maintenance.source` / `maintenance.sync` | 空对象 |
| `maintenance.save` | `server,name,starts_at`，可选 `ends_at`，RFC3339 时间 |
| `maintenance.delete` | `server` |
| `publish.status` | 空对象 |

| 模板服务方法 | 参数 |
|---|---|
| `state.get` / `state.subscribe` / `status.get` / `ping` | 空对象 |
| `templates.list` | 可选 `kind` formal/temporary、`limit` 1–200 默认50、`offset` 默认0 |
| `templates.import` | `item_id,name,image_base64`，可选 `tag` |
| `mappings.save` | `unknown_id,item_id,name`，可选 `tag` |
| `publish` | 可选 `note`，返回排队任务 ID |
| `releases.list` | 空对象 |
| `icons.get` | `sha256`，返回 base64 图标 |

主服务单帧上限 64 KiB、每连接每秒10个命令/突发50、最多4个并行命令，单次最长100秒。模板服务单帧上限512 KiB、每连接每秒10个命令/突发50，顺序执行命令。状态变化通过连接推送，页面不做 HTTP 轮询。公开统计页面的 HTTP 请求不携带管理凭据。

服务间事件使用单独的 HTTP POST 和事件凭据，属于后端通讯，不属于管理前端 API。
