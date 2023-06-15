# Build the manager binary
FROM --platform=$TARGETPLATFORM golang:1.19.9 as builder
ARG TARGETARCH
ARG TARGETOS

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY lib/ lib/

# Build
RUN CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH GO111MODULE=on go build -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/cc:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=alpine/git /usr/bin/git /usr/bin/git
USER 65532:65532

ENTRYPOINT ["/manager"]
