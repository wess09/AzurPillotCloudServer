# 模板服务器镜像：Go 静态二进制 + Node 22 + Wrangler。
# 该镜像自带 wrangler，因此 compose 中把 TEMPLATE_WRANGLER_IMAGE 设为空，
# 发布时直接调用容器内的 wrangler，无需挂载宿主机 Docker socket。
FROM --platform=$BUILDPLATFORM node:22-alpine AS web

ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /web
COPY template-web/package.json template-web/package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" && npm ci
COPY template-web/ ./
RUN npm run build -- --outDir dist

FROM golang:1.26-alpine AS build

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist internal/templateserver/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/templateserver ./cmd/templateserver

FROM node:22-alpine

ARG WRANGLER_VERSION=4
ARG NPM_REGISTRY=https://registry.npmjs.org
RUN npm config set registry "$NPM_REGISTRY" \
    && npm install -g "wrangler@${WRANGLER_VERSION}" \
    && npm cache clean --force \
    && adduser -D -H -u 10001 app

COPY --from=build /out/templateserver /usr/local/bin/templateserver

USER app
EXPOSE 8090
ENTRYPOINT ["templateserver"]
