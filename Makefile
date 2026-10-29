.PHONY: build test race vet lint run web-install web-dev web-build template-web-install template-web-dev template-web-build template-server docker-up docker-down clean

build:
	go build -o bin/ap-cloud-server$(shell go env GOEXE) ./cmd/server

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

# 完整集成测试：需要本机 PostgreSQL，并设置 TEST_DATABASE_URL
test-integration:
	TEST_DATABASE_URL="$${TEST_DATABASE_URL:-postgres://postgres:postgres@localhost:5432/apcloud_test?sslmode=disable}" go test -race -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run

run:
	go run ./cmd/server

web-install:
	cd web && npm install

web-dev:
	cd web && npm run dev

# 构建前端并输出到 internal/adminui/dist（随后重新 go build 即完成嵌入）
web-build:
	cd web && npm run build

template-web-install:
	cd template-web && npm install

template-web-dev:
	cd template-web && npm run dev

# 构建模板管理台并输出到 internal/templateserver/webui/dist
template-web-build:
	cd template-web && npm run build

# 模板服务器二进制（需先 template-web-build）
template-server:
	go build -o bin/ap-template-server$(shell go env GOEXE) ./cmd/templateserver

# 一键构建包含两个管理界面的完整二进制
all: web-build template-web-build build

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

clean:
	rm -rf bin dist
