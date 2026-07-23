# Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
# Copyright © 2021-2022, 2025 Hewlett Packard Enterprise Development LP
#
# SPDX-License-Identifier: MIT

# Dockerfile for the console-data service

### goreleaser Stage ###
### Assume goreleaser has already compiled the binary and written it to ./remote-console

FROM ubuntu:26.04 AS ubuntu-goreleaser

ARG TARGETPLATFORM

RUN apt -y update
RUN apt -y install conman less vim ssh jq tar procps inotify-tools

COPY ${TARGETPLATFORM}/remote-console /app/
COPY scripts/conman.conf.tmpl /app/conman.conf.tmpl
COPY scripts/ssh-key-console /usr/bin/
COPY scripts/ssh-pwd-console /usr/bin/
COPY configs /app/configs

RUN chown -Rv 65534:65534 /app /etc/conman.conf
USER 65534:65534

RUN echo 'alias ll="ls -l"' > /app/bashrc
RUN echo 'alias vi="vim"' >> /app/bashrc

ENTRYPOINT ["/app/remote-console"]


# Build Stage: Build the Go binary
FROM golang:1.26-bookworm AS builder

RUN set -eux \
    && apt-get update \
    && apt-get install -y --no-install-recommends build-essential

# Configure go env
ENV GOPATH=/usr/local/golib
RUN export GOPATH=$GOPATH
RUN go env -w GO111MODULE=auto

# Copy source files
COPY cmd $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/cmd
COPY configs configs
COPY scripts scripts
COPY internal $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/internal
COPY go.mod $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/go.mod
COPY go.sum $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/go.sum

# Build the image
RUN set -ex && go build -C $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/cmd/remote-console -v -o /usr/local/bin/remote-console

### Final Stage ###
FROM ubuntu:26.04 AS final

# Install needed packages
RUN set -eux \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ipmitool \
        libfreeipmi17 \
        libipmiconsole2 \
        iputils-ping \
        coreutils \
        conman \
        libcap2-bin \
        expect \
        openssh-client \
        sshpass \
        vim \
        bash \
        jq \
        inotify-tools \
        logrotate \
    && rm -rf /var/lib/apt/lists/*

# Copy in the needed files
COPY --from=builder /usr/local/bin/remote-console /app/
COPY scripts/conman.conf.tmpl /app/conman.conf.tmpl
COPY scripts/ssh-key-console /usr/bin/
COPY scripts/ssh-pwd-console /usr/bin/
COPY configs /app/configs

# Aliases
RUN echo 'alias ll="ls -l"' >> /root/.bashrc
RUN echo 'alias vi="vim"' >> /root/.bashrc
RUN chmod +775 /usr/bin/ssh-key-console /usr/bin/ssh-pwd-console

# Create log directories and set ownership to nobody (UID/GID 65534)
RUN mkdir -p /var/log/conman/ /var/log/conman.old/ \
    && chown -Rv 65534:65534 /app /var/log/conman/ /var/log/conman.old/

USER 65534:65534

ENTRYPOINT ["/app/remote-console"]
