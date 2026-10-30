.PHONY: build test race vet lint run all docker-up docker-down
build:
	go build -o bin/ap-cloud-server$(shell go env GOEXE) ./cmd/server
test:
	go test -count=1 ./...
race:
	go test -race -count=1 ./...
vet:
	go vet ./...
lint:
	golangci-lint run
run:
	go run ./cmd/server
all: build
docker-up:
	docker compose up -d --build
docker-down:
	docker compose down
