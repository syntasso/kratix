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
    go build -o /out/quick-start-installer main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/cc:nonroot

COPY --chown=nonroot:nonroot --from=builder /out/quick-start-installer /bin/quick-start-installer
COPY --chown=nonroot:nonroot --from=ghcr.io/syntasso/kratix-pipeline-utility:v0.0.1 /bin/kubectl-cli /usr/local/bin/kubectl

USER 65532:65532
ENTRYPOINT ["/bin/quick-start-installer"]
