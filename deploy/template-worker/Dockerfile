# 固定版本的 Wrangler 镜像，供模板服务器以 `docker run` 调用。
# 模板服务器在 CF_DEPLOY=true 且 TEMPLATE_WRANGLER_IMAGE 非空时使用它。
FROM node:22-alpine

ARG WRANGLER_VERSION=4
RUN npm install -g "wrangler@${WRANGLER_VERSION}" \
    && npm cache clean --force

WORKDIR /app

# 不要在镜像上设置 ENTRYPOINT：publish.go 显式传入 `wrangler deploy`。
CMD ["wrangler", "--version"]
