# SPDX-License-Identifier: AGPL-3.0-only
# Provenance-includes-location: https://github.com/cortexproject/cortex/cmd/cortex/Dockerfile
# Provenance-includes-license: Apache-2.0
# Provenance-includes-copyright: The Cortex Authors.

# Build phase
FROM --platform=linux/amd64 golang:1.26.0-alpine3.23 AS builder

ARG BUILDPLATFORM
ARG TARGETARCH
ARG TARGETOS
ARG GIT_BRANCH
ARG GIT_REVISION
ARG VERSION
ENV GOARCH=${TARGETARCH} GOOS=${TARGETOS}
WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker cache
COPY go.mod go.sum ./

# Copy source code
COPY . .
RUN apk update --no-cache && apk upgrade --no-cache && \
    apk add build-base cmake clang clang-dev llvm llvm-dev lz4-dev zstd-dev clang-extra-tools git && \
    rm -rf /var/cache/apk/*
RUN cd vendor/github.com/boris-chu/go-openzl/ && rm -rf vendor && mkdir vendor && cd vendor && \
    git clone https://github.com/facebook/openzl.git && cd openzl && git checkout v0.1.0 && \
    cd ../../ && make make check-openzl && make build-openzl && make build
RUN cd ../../../../../../  && ls -al && \
    cd pkg/util/grpcencoding/zstd && go test . && \
    cd ../openzl && go test .
RUN exit

RUN CGO_ENABLED=1 GOOS=${GOOS} GOARCH=${GOARCH} go build \
    -ldflags " -X github.com/grafana/mimir/pkg/util/version.Branch=${GIT_BRANCH} -X github.com/grafana/mimir/pkg/util/version.Revision=${GIT_REVISION} -X github.com/grafana/mimir/pkg/util/version.Version=${VERSION} -extldflags \"-static\" -s -w" \
    -tags netgo,stringlabels -o "cmd/mimir/mimir_${TARGETOS}_${TARGETARCH}" ./cmd/mimir

# Runtime phase
FROM       alpine:3.23.3
ARG        EXTRA_PACKAGES
RUN        apk update && apk upgrade --no-cache && rm -rf /var/cache/apk/*
RUN        apk add --no-cache ca-certificates tzdata $EXTRA_PACKAGES
# Expose TARGETOS and TARGETARCH variables. These are supported by Docker when using BuildKit, but must be "enabled" using ARG.
ARG        TARGETOS
ARG        TARGETARCH
# Set to non-empty value to use ${BINARY_SUFFIX} when copying mimir binary, leave unset to use no suffix.
COPY       --from=builder /app/cmd/mimir/mimir_${TARGETOS}_${TARGETARCH} /bin/mimir

USER root
RUN apk update --no-cache && apk upgrade --no-cache && rm -rf /var/cache/apk/*
RUN addgroup -g 10001 -S mimir && \
    adduser -u 10001 -S mimir -G mimir --disabled-password
RUN mkdir -p /etc/mimir && \
    mkdir -p /data/rules && mkdir -p /data/tokens \
    mkdir -p /data/alertmanager && mkdir -p /data/compactor \
    mkdir -p /var/mimir && \
    mkdir -p /active-query-tracker && \
    chown -R mimir:mimir /etc/mimir /data && \
    chown -R mimir:mimir /var/mimir /active-query-tracker
USER 10001

EXPOSE     8080
ENTRYPOINT [ "/bin/mimir" ]

ARG revision
LABEL org.opencontainers.image.title="mimir" \
    org.opencontainers.image.source="https://github.com/grafana/mimir/tree/main/cmd/mimir" \
    org.opencontainers.image.revision="${revision}"

