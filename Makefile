MODULE := github.com/joechenrh/golem
BINARY := golem

.PHONY: build run test test-integration lint clean

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

clean:
	rm -rf bin/
