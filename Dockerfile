FROM --platform=$BUILDPLATFORM golang:1.23 AS builder
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
    --mount=type=cache,target=/go/pkg \
    go build -a -o /out/manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
# Use a distroless base image that supports arbitrary UIDs
FROM gcr.io/distroless/cc
WORKDIR /
COPY --from=builder /out/manager .
COPY --from=alpine/git /usr/bin/git /usr/bin/git

# Do not specify a user so OpenShift can assign a project-specific UID

ENTRYPOINT ["/manager"]
