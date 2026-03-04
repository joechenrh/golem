MODULE := github.com/joechenrh/golem
BINARY := golem

.PHONY: build run test test-integration lint check fmt clean

build:
	go build -o bin/$(BINARY) ./cmd/golem/

run: build
	./bin/$(BINARY)

test:
	go test ./...

test-integration:
	go test -tags=integration ./internal/agent/

lint:
	golangci-lint run ./...

check:
	@echo "==> gofmt"
	@test -z "$$(gofmt -l .)" || (echo "Files not formatted:"; gofmt -l .; exit 1)
	@echo "==> go vet"
	@go vet ./...
	@echo "==> go test"
	@go test ./...
	@echo "All checks passed."

fmt:
	gofmt -w .

clean:
	rm -rf bin/
