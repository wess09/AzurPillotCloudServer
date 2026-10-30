# 独立模板服务接入

模板 API 仓库为独立 `AzurPilotCloud`，主 API 仓库为 `AzurPillotCloudServer`，前端为新建 `wess09/AzurPilotCloudWeb`。三个代码与部署边界独立。

客户端直接上传到 `https://templates-api.azurpilot.com/api/v3/post/templates/unknown` 或批量 `/api/v3/post/unknown-candidates`。资源直接读取 `https://templates.azurpilot.com/latest.json` 及其签名清单、映射、增量和图标。主 API 没有模板图像路由或代理。

管理员访问 `https://admin.azurpilot.com`，通过 [WebSocket 协议](admin-websocket.md) 分别管理两个后端。模板服务与主服务配置相同管理员令牌；精确 Origin 为 `https://admin.azurpilot.com`。没有第二套模板后台登录。

主服务唯一模板入口为 `POST /internal/v1/templates/events`，由 `TEMPLATE_EVENT_TOKEN` 认证。模板服务配置 `MAIN_EVENT_URL=https://api.azurpilot.com/internal/v1/templates/events`。只传映射变化和状况，不传图像；主库只留映射、事件回执和版本元数据。

`release.published` 事件事务提交后返回相同 event_id 的 ack。重复事件幂等，同 ID 异正文拒绝，旧版本不会覆盖新映射；模板 outbox 按序重试。原始统计保留 U-ID，受影响小时立即排队重新聚合，正式结果保留所有历史数量。

公开统计独立部署到 `https://stats.azurpilot.com`，仅调用主服务 v3 GET 接口。两个 Cloudflare Pages 项目直接连接新前端仓库的 main 分支；不上传本地构建目录部署。

模板完整协议见独立模板仓库 `docs/protocol.md`，部署配置见其 `docs/integration.md`。
