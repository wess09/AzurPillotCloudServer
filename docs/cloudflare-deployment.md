# Cloudflare 与后端部署

前端仓库：`https://github.com/wess09/AzurPilotCloudWeb`，生产分支main。

| Pages 项目 | 构建命令 | 输出 | 自定义域名 |
|---|---|---|---|
| azurpilot-admin | npm run build:admin | dist/admin | admin.azurpilot.com |
| azurpilot-stats | npm run build:stats | dist/stats | stats.azurpilot.com |

两个项目均使用 GitHub 原生关联自动部署。更新前端必须提交并推送仓库，然后等待 Cloudflare Git 构建成功；不使用手动目录上传或 Pages 手工部署命令。构建环境为Node22，根目录为仓库根目录。主服务器不保存前端资源。

后端 `api.azurpilot.com` 保留主机 API；`templates-api.azurpilot.com` 保留 NAT 机独立 API。代理必须支持 WebSocket Upgrade，使用精确来源白名单。

模板静态资源由独立模板服务发布到 `azurpilot-templates` Worker，域名 `templates.azurpilot.com`。这是模板数据发布通道，与前端代码的 Git 构建不同。公告和强制更新文档继续由各自静态 Worker 分发。

部署凭据仅存服务端.env或连接账户。模板签名私钥持久化保存在模板机 `data/signing.key`，升级不重新生成。后端镜像启动先执行 ORM 迁移，再启动服务。
