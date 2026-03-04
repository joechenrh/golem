MODULE := github.com/joechenrh/golem
BINARY := golem

.PHONY: build run test lint clean

build:
	go build -o bin/$(BINARY) ./cmd/golem/

run: build
	./bin/$(BINARY)

test:
	go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/
