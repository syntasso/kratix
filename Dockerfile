FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETARCH
ARG TARGETOS

WORKDIR /workspace

ENV GO111MODULE=on
ENV CGO_ENABLED=0
ENV GOOS=${TARGETOS}
ENV GOARCH=${TARGETARCH}

RUN --mount=target=. \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /out/manager cmd/main.go

RUN --mount=target=. \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /out/pipeline-adapter work-creator/*.go

FROM debian:bookworm-slim AS git-deps
RUN apt-get update && \
    apt-get install -y --no-install-recommends git openssh-client ca-certificates && \
    rm -rf /var/lib/apt/lists/*

FROM busybox AS busybox

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /

COPY --from=builder /out/manager /manager
COPY --from=builder /out/pipeline-adapter /bin/pipeline-adapter

# shell + env for scripts
COPY --chown=nonroot:nonroot --from=busybox /usr/bin/env /usr/bin/env
COPY --chown=nonroot:nonroot --from=busybox /bin/sh /bin/sh

# git + ssh + runtime deps
COPY --from=git-deps /usr/bin/git /usr/bin/git
COPY --from=git-deps /usr/bin/ssh /usr/bin/ssh
COPY --from=git-deps /usr/lib/git-core /usr/lib/git-core
COPY --from=git-deps /usr/lib /usr/lib
COPY --from=git-deps /lib /lib
COPY --from=git-deps /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 65532:65532
ENTRYPOINT ["/manager"]
