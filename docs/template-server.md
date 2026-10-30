# 模板服务已独立

本仓库不再包含 Go 模板服务器、模板 WebUI、图像存储、模板上传或管理代理。旧部署文件已移除。

实际服务位于独立 `AzurPilotCloud` 仓库；数据库通过 SQLAlchemy/Alembic 管理。统一管理页面位于新建 `AzurPilotCloudWeb` 仓库，在 Cloudflare 托管。

参阅 [独立服务接入](template-service-integration.md) 和 [管理协议](admin-websocket.md)。
