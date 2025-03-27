#!/bin/sh
#
# MIT License
#
# (C) Copyright 2021-2022 Hewlett Packard Enterprise Development LP
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
set -x
export COMPOSE_FILE=docker-compose-devel.yaml

echo "Compose project name: $COMPOSE_PROJECT_NAME"

# It's possible we don't have docker-compose, so if necessary bring our own.
docker_compose_exe=$(command -v docker-compose)
if ! [[ -x "$docker_compose_exe" ]]; then
  if ! [[ -x "./docker-compose" ]]; then
    echo "Getting docker-compose..."
    curl -L "https://github.com/docker/compose/releases/download/1.23.2/docker-compose-$(uname -s)-$(uname -m)" \
      -o ./docker-compose

    if [[ $? -ne 0 ]]; then
      echo "Failed to fetch docker-compose!"
      exit 1
    fi

    chmod +x docker-compose
  fi
  docker_compose_exe="./docker-compose"
fi

# Start the services
echo "Starting containers..."
${docker_compose_exe} up -d --build
