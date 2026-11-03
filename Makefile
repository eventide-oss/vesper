.PHONY: build test vet lint

build:
	go build -o bin/vesperctl ./cmd/vesperctl

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run
