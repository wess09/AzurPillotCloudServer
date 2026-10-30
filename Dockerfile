# Backend only: frontend is built from the independent GitHub repository by Cloudflare.
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

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
