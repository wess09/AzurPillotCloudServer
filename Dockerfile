# 多阶段构建：前端（React）→ 后端（Go，内嵌前端产物）→ 运行镜像。
# 国内构建可用 --build-arg NPM_REGISTRY=... --build-arg GOPROXY=... --build-arg RUNTIME_IMAGE=...
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM --platform=$BUILDPLATFORM node:22-alpine AS web

ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm config set registry "$NPM_REGISTRY" && npm ci
COPY web/ ./
# 覆盖 vite 配置的仓库内相对输出目录，产物落在镜像本层便于拷贝。
RUN npm run build -- --outDir dist

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# 先拷贝依赖清单，利用层缓存。
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 前端产物来自 web 构建阶段。
COPY --from=web /web/dist internal/adminui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w -X ap-cloud-server/internal/version.Version=${VERSION}" \
    -o /out/ap-cloud-server ./cmd/server && mkdir -p /out/public

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/ap-cloud-server /ap-cloud-server
COPY --from=build --chown=65532:65532 /out/public /app/public
WORKDIR /app
VOLUME ["/app/public"]
ENV PUBLISH_DIR=/app/public
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/ap-cloud-server"]
