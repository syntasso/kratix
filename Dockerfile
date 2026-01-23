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


# Using alpine/git image as base, as it contains the /usr/bin/git binary needed
# by the Kratix Git Writer. It also contains the /usr/bin/env and /bin/sh
# binaries needed to use the image as the pipeline-adapter image.
FROM alpine/git
WORKDIR /

COPY --from=builder /out/manager .
COPY --from=builder /out/pipeline-adapter /bin/pipeline-adapter

USER 65532:65532

ENTRYPOINT ["/manager"]