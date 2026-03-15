.PHONY: build vet test

build:
	go build ./cmd/claude-monitor

vet:
	go vet ./...

test:
	go test ./...
