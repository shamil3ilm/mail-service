# mail-service — dev tasks
# Windows note: use `make` from Git Bash / WSL, or run the equivalent commands
# from PowerShell (see README).

BIN      := mailservice
BUILD    := go build -trimpath -ldflags="-s -w -X main.version=$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)"
PKG_MAIN := ./cmd/mailservice

.PHONY: help
help:
	@echo "targets:"
	@echo "  run       - go run the service with .env loaded"
	@echo "  build     - build binary into ./bin/"
	@echo "  test      - go test ./... with race"
	@echo "  vet       - go vet ./..."
	@echo "  tidy      - go mod tidy"
	@echo "  cover     - test + coverage.html"
	@echo "  docker    - docker compose up --build"
	@echo "  clean     - remove ./bin/ and ./data/"

.PHONY: run
run:
	MAIL_LOG_FORMAT=text go run $(PKG_MAIN)

.PHONY: dev
dev:
	@echo "PowerShell: .\\dev.ps1"
	@echo "Bash:       (install air) air"

.PHONY: build
build:
	mkdir -p bin
	$(BUILD) -o bin/$(BIN) $(PKG_MAIN)

.PHONY: test
test:
	go test -race -count=1 ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: cover
cover:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html"

.PHONY: docker
docker:
	docker compose -f deploy/docker-compose.yml up --build

.PHONY: clean
clean:
	rm -rf bin data coverage.out coverage.html
