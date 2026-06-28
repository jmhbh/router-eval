GO ?= go
GOCACHE ?= $(CURDIR)/.cache/go-build
BIN_DIR ?= bin
CLI := $(BIN_DIR)/router-eval

.PHONY: build test clean

build:
	mkdir -p $(BIN_DIR)
	GOCACHE=$(GOCACHE) $(GO) build -o $(CLI) ./cmd/router-eval

test:
	GOCACHE=$(GOCACHE) $(GO) test ./...

clean:
	rm -rf $(BIN_DIR) .cache
