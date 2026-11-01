.PHONY: all build test coverage lint clean install openwrt help

BINARY_NAME=updns
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"
OUTPUT_DIR=bin
OPENWRT_SDK?=

all: test build

build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(OUTPUT_DIR)
	@cd cmd/updns && go build $(LDFLAGS) -o ../../$(OUTPUT_DIR)/$(BINARY_NAME) .

test:
	@echo "Running tests..."
	@go test -v -race ./...

coverage:
	@echo "Running tests with coverage..."
	@go test -v -race -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

clean:
	@echo "Cleaning..."
	@rm -rf $(OUTPUT_DIR)
	@rm -f coverage.out coverage.html

lint:
	@echo "Running gofmt..."
	@gofmt -w $(shell find cmd config server resolver rules cache nftset -name '*.go')
	@echo "Running go vet..."
	@go vet ./...

install: build
	@echo "Installing $(BINARY_NAME) to /usr/local/bin..."
	@cp $(OUTPUT_DIR)/$(BINARY_NAME) /usr/local/bin/

openwrt:
	@test -n "$(OPENWRT_SDK)" || (echo "Set OPENWRT_SDK to an OpenWrt SDK directory" >&2; exit 2)
	@./scripts/build-openwrt.sh "$(OPENWRT_SDK)"

help:
	@echo "Available targets:"
	@echo "  make build       - Build the binary"
	@echo "  make test        - Run tests"
	@echo "  make coverage    - Run tests with coverage report"
	@echo "  make lint        - Run gofmt and go vet"
	@echo "  make clean       - Clean build artifacts"
	@echo "  make install     - Build and install to /usr/local/bin"
	@echo "  make openwrt OPENWRT_SDK=/path/to/sdk - Build OpenWrt APK packages"
	@echo "  make all         - Run tests and build (default)"
	@echo "  make help        - Show this help message"
