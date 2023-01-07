#
# Copyright (c) 2018
# Cavium
#
# SPDX-License-Identifier: Apache-2.0
#

ARG BUILDER_BASE=cr.loongnix.cn/library/golang:1.19-alpine
FROM ${BUILDER_BASE} AS builder

WORKDIR /edgex-go

RUN apk add --update --no-cache make bash git gcc musl-dev

COPY go.mod vendor* ./

COPY . .
# Build the SMA executable.
RUN make cmd/sys-mgmt-agent/sys-mgmt-agent

# Build the golang "executor" executable.
RUN make cmd/sys-mgmt-executor/sys-mgmt-executor

# Get the Docker-in-Docker image layered-in now.
FROM cr.loongnix.cn/library/alpine:3.11

LABEL license='SPDX-License-Identifier: Apache-2.0' \
      copyright='Copyright (c) 2017-2019: Mainflux, Cavium, Dell, Copyright (c) 2021: Intel Corporation'

# consul token needs to be security-bootstrappable and for security-bootstrappable, dumb-init is required
RUN apk add --update --no-cache bash dumb-init py3-pip curl docker-ce-cli && \
      pip3 install --no-cache-dir docker-compose==1.23.2 -i http://pypi.douban.com/simple/ --trusted-host pypi.douban.com

ENV APP_PORT=58890
#expose system mgmt agent port
EXPOSE $APP_PORT

# Copy over the SMA executable bits.
COPY --from=builder /edgex-go/cmd/sys-mgmt-agent/sys-mgmt-agent /
COPY --from=builder /edgex-go/cmd/sys-mgmt-agent/res/configuration.toml /res/configuration.toml

# Copy over the golang "executor" executable.
COPY --from=builder /edgex-go/cmd/sys-mgmt-executor/sys-mgmt-executor /

ENTRYPOINT ["/sys-mgmt-agent"]
CMD ["-cp=consul.http://edgex-core-consul:8500", "--registry"]

# Following (commented-out) line is a life-saving DEBUG statement.
# ENTRYPOINT tail -f /dev/null
