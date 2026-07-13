# Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
# Copyright © 2021-2022, 2025 Hewlett Packard Enterprise Development LP
#
# SPDX-License-Identifier: MIT

NAME    ?= remote-console
VERSION ?= $(shell git describe --tags --always --abbrev=0)
GOLANGCI_LINT ?= golangci-lint
GO_PACKAGES ?= ./...
TEST_PACKAGES ?= $(GO_PACKAGES)
FABRICA ?= go run github.com/openchami/fabrica/cmd/fabrica
FABRICA_GENERATE_ARGS ?=

.PHONY: all lint test image generate format-generated

all : lint image


lint:
		$(GOLANGCI_LINT) run $(GO_PACKAGES)

test:
		go test $(TEST_PACKAGES)

image:
		docker build --pull $(DOCKER_ARGS) --tag '$(NAME):$(VERSION)' .

generate:
		$(FABRICA) generate $(FABRICA_GENERATE_ARGS)
		$(MAKE) format-generated

format-generated:
		@files=$$(find apis cmd/server pkg/resources -name '*.go' -type f 2>/dev/null); \
		if [ -n "$$files" ]; then gofmt -w $$files; fi
