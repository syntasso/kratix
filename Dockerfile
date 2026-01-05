FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETARCH
ARG TARGETOS

WORKDIR /workspace

ENV GO111MODULE=on
ENV CGO_ENABLED=0
ENV GOOS=${TARGETOS}
ENV GOARCH=${TARGETARCH}

# Build
RUN --mount=target=. \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /out/manager cmd/main.go

RUN --mount=target=. \
    --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -o /out/pipeline-adapter work-creator/*.go

FROM busybox AS busybox

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/cc:nonroot
WORKDIR /
COPY --from=builder /out/manager .
COPY --from=alpine/git /usr/bin/git /usr/bin/git

COPY --from=builder /out/pipeline-adapter /bin/pipeline-adapter
COPY --chown=nonroot:nonroot --from=busybox /usr/bin/env /usr/bin/env
COPY --chown=nonroot:nonroot --from=busybox /bin/sh /bin/sh

USER 65532:65532

ENTRYPOINT ["/manager"]
