# Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
# Copyright © 2021-2022, 2025 Hewlett Packard Enterprise Development LP
#
# SPDX-License-Identifier: MIT

NAME    ?= remote-console
VERSION ?= $(shell git describe --tags --always --abbrev=0)
GOLANGCI_LINT ?= golangci-lint
GO_PACKAGES ?= ./...
TEST_PACKAGES ?= $(GO_PACKAGES)

.PHONY: all lint test image

all : lint image


lint:
		$(GOLANGCI_LINT) run $(GO_PACKAGES)

test:
		go test $(TEST_PACKAGES)

image:
		docker build --pull $(DOCKER_ARGS) --tag '$(NAME):$(VERSION)' .
