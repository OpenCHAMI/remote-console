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
FROM docker.io/library/golang:1.24-alpine AS build
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

### Final Stage ###
# Start with a fresh image so build tools are not included
FROM docker.io/alpine:3 AS base

# Copy in the needed files
COPY --from=build /usr/local/bin/remote-console /app/
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
