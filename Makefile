.PHONY: build run test lint tidy

build:
	go build -o bench ./cmd/bench

run:
	go run ./cmd/bench -t SyncImplicit -t SyncSessionsImplicit \
		-url $(NEO4J_URL) -usr $(NEO4J_USERNAME) -pwd $(NEO4J_PASSWORD) \
		-db $(NEO4J_DATABASE) -n 10

test:
	go test -race ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
