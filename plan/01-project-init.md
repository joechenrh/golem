# Step 1: Project Initialization

## Scope

Set up Go module, directory structure, build system, and environment config template.

## Files to Create

- `go.mod`
- `Makefile`
- `.env.example`
- All empty directories (via placeholder or first file creation)

## Key Points

### go.mod

```
module github.com/joechenrh/golem
go 1.23
```

### Dependencies

```
github.com/joho/godotenv       # .env file loading
github.com/google/uuid         # UUIDs for tape entries
go.uber.org/zap                # Structured logging
```

Telegram/Lark/mnemos dependencies are NOT added yet — only when those phases are implemented.

### Makefile

```makefile
MODULE := github.com/joechenrh/golem
BINARY := golem

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
```

### .env.example

```bash
# LLM Configuration
GOLEM_MODEL=openai:gpt-4o
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...

# Agent Settings
GOLEM_MAX_TOOL_ITER=15
GOLEM_SHELL_TIMEOUT=30s
GOLEM_TAPE_DIR=~/.golem/tapes
GOLEM_LOG_LEVEL=info

# Telegram (Phase 3)
# TELEGRAM_BOT_TOKEN=
# TELEGRAM_ALLOW_FROM=

# Lark (Phase 4)
# LARK_APP_ID=
# LARK_APP_SECRET=
# LARK_VERIFY_TOKEN=
# LARK_WEBHOOK_PORT=9999

# mnemos Memory (Phase 5)
# MNEMOS_URL=http://localhost:8080
# MNEMOS_SPACE_ID=default
```

### Directory Structure

All directories are created implicitly when files are written in subsequent steps. The full tree:

```
golem/
├── cmd/golem/
├── internal/
│   ├── agent/
│   ├── channel/
│   │   ├── cli/
│   │   ├── telegram/
│   │   └── lark/
│   ├── config/
│   ├── llm/
│   ├── memory/
│   ├── router/
│   ├── shell/
│   ├── tape/
│   └── tools/
│       └── builtin/
├── plan/
├── go.mod
├── .env.example
└── Makefile
```

## Done When

- `go mod tidy` succeeds
- `make build` compiles (even if main.go is just `package main; func main() {}`)
