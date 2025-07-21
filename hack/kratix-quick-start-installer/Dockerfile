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
    --mount=type=cache,target=/go/pkg \
    go build -a -o /out/quick-start-installer main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/cc

COPY --chown=nonroot:nonroot --from=builder /out/quick-start-installer /bin/quick-start-installer
COPY --chown=nonroot:nonroot --from=bitnami/kubectl /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/kubectl

# Do not specify a user so OpenShift can assign a project-specific UID
ENTRYPOINT ["/bin/quick-start-installer"]
