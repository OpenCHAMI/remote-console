#
# MIT License
#
# (C) Copyright 2021-2022, 2025 Hewlett Packard Enterprise Development LP
#
# Permission is hereby granted, free of charge, to any person obtaining a
# copy of this software and associated documentation files (the "Software"),
# to deal in the Software without restriction, including without limitation
# the rights to use, copy, modify, merge, publish, distribute, sublicense,
# and/or sell copies of the Software, and to permit persons to whom the
# Software is furnished to do so, subject to the following conditions:
#
# The above copyright notice and this permission notice shall be included
# in all copies or substantial portions of the Software.
#
# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
# IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
# FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
# THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR
# OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
# ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
# OTHER DEALINGS IN THE SOFTWARE.
#
# Dockerfile for the console-data service

# Build will be where we build the go binary
FROM docker.io/library/golang:1.24-alpine AS build-alpine
RUN set -eux \
    && apk add --upgrade --no-cache apk-tools \
    && apk update \
    && apk add build-base \
    && apk -U upgrade --no-cache

# Configure go env - installed as package but not quite configured
ENV GOPATH=/usr/local/golib
RUN export GOPATH=$GOPATH

# set up go env
RUN go env -w GO111MODULE=auto

# Build the image
COPY cmd $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/cmd
COPY configs configs
COPY scripts scripts
COPY internal $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/internal
COPY go.mod $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/go.mod
COPY go.sum $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/go.sum

RUN set -ex  && go build -C $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/cmd/remote-console -v -tags musl -o /usr/local/bin/remote-console

FROM docker.io/library/golang:1.24 AS build

# Configure go env - installed as package but not quite configured
ENV GOPATH=/usr/local/golib
RUN export GOPATH=$GOPATH

# set up go env
RUN go env -w GO111MODULE=auto

# Build the image
COPY cmd $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/cmd
COPY configs configs
COPY scripts scripts
COPY internal $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/internal
COPY go.mod $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/go.mod
COPY go.sum $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/go.sum

RUN set -ex  && go build -C $GOPATH/src/github.com/OpenCHAMI/remote-console/v2/cmd/remote-console -v -o /usr/local/bin/remote-console

### Alpine image ###
# Start with a fresh image so build tools are not included
FROM docker.io/alpine:3 AS alpine

# Copy in the needed files
COPY --from=build-alpine /usr/local/bin/remote-console /app/
COPY scripts/conman.conf /app/conman_base.conf
COPY scripts/conman.conf /etc/conman.conf
COPY scripts/ssh-key-console /usr/bin
COPY scripts/ssh-pwd-console /usr/bin
COPY configs /app/configs

# Install needed packages
# NOTE: setcap allows non-root users to bind to port 80 for a specific application
RUN set -eux \
    && apk add --upgrade --no-cache apk-tools \
    && apk add --upgrade --no-cache coreutils \
    && apk add --upgrade --no-cache tini \
    && apk update \
    && apk add --no-cache libcap \
    && apk -U upgrade --no-cache \
    && setcap 'cap_net_bind_service=+ep' /app/remote-console

RUN echo 'alias ll="ls -l"' > ~/.bashrc
RUN echo 'alias vi="vim"' >> ~/.bashrc

# set to run as user 'nobody'
# Change ownership of the app dir and switch to user 'nobody'
RUN mkdir /var/log/conman/
RUN mkdir /var/log/conman.old/
RUN chown -Rv 65534:65534 /app /etc/conman.conf /var/log/conman/ /var/log/conman.old/
USER 65534:65534

CMD [ "/app/remote-console" ]

ENTRYPOINT [ "/sbin/tini", "--" ]

### Alma UBI ###

FROM almalinux/9-minimal:9.6 AS alma

RUN microdnf -y install epel-release
RUN microdnf -y install conman less vim openssh jq tar procps inotify-tools
RUN microdnf -y clean all

COPY --from=build /usr/local/bin/remote-console /app/
COPY scripts/conman.conf /app/conman_base.conf
COPY scripts/conman.conf /etc/conman.conf
COPY scripts/ssh-key-console /usr/bin
COPY scripts/ssh-pwd-console /usr/bin
COPY configs /app/configs

RUN chown -Rv 65534:65534 /app /etc/conman.conf
USER 65534:65534

RUN echo 'alias ll="ls -l"' > /app/bashrc
RUN echo 'alias vi="vim"' >> /app/bashrc

ENTRYPOINT ["/app/remote-console"]

### Ubuntu

FROM ubuntu:plucky AS ubuntu

RUN apt -y update
RUN apt -y install conman less vim ssh jq tar procps inotify-tools

COPY --from=build /usr/local/bin/remote-console /app/
COPY scripts/conman.conf /app/conman_base.conf
COPY scripts/conman.conf /etc/conman.conf
COPY scripts/ssh-key-console /usr/bin
COPY scripts/ssh-pwd-console /usr/bin
COPY configs /app/configs

RUN chown -Rv 65534:65534 /app /etc/conman.conf
USER 65534:65534

RUN echo 'alias ll="ls -l"' > /app/bashrc
RUN echo 'alias vi="vim"' >> /app/bashrc

ENTRYPOINT ["/app/remote-console"]

### Ubuntu, without build copy
### Assume goreleaser has already compiled the binary and written it to ./remote-console

FROM ubuntu:plucky AS ubuntu-goreleaser

RUN apt -y update
RUN apt -y install conman less vim ssh jq tar procps inotify-tools

COPY remote-console /app/
COPY scripts/conman.conf /app/conman_base.conf
COPY scripts/conman.conf /etc/conman.conf
COPY scripts/ssh-key-console /usr/bin
COPY scripts/ssh-pwd-console /usr/bin
COPY configs /app/configs

RUN chown -Rv 65534:65534 /app /etc/conman.conf
USER 65534:65534

RUN echo 'alias ll="ls -l"' > /app/bashrc
RUN echo 'alias vi="vim"' >> /app/bashrc

ENTRYPOINT ["/app/remote-console"]
