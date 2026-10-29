# 模板服务器镜像：Go 静态二进制 + Node 22 + Wrangler。
# 该镜像自带 wrangler，因此 compose 中把 TEMPLATE_WRANGLER_IMAGE 设为空，
# 发布时直接调用容器内的 wrangler，无需挂载宿主机 Docker socket。
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/templateserver ./cmd/templateserver

FROM node:22-alpine

ARG WRANGLER_VERSION=4
RUN npm install -g "wrangler@${WRANGLER_VERSION}" \
    && npm cache clean --force \
    && adduser -D -H -u 10001 app

COPY --from=build /out/templateserver /usr/local/bin/templateserver

USER app
EXPOSE 8090
ENTRYPOINT ["templateserver"]
