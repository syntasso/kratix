FROM --platform=${TARGETPLATFORM} golang:1.21 as builder
ARG TARGETARCH
ARG TARGETOS

WORKDIR /workspace
# Copy the Go Modules manifests
COPY * /workspace/
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Build work-creator binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -a main.go

FROM --platform=${TARGETPLATFORM} alpine
# Use rancher/kubectl to get ./kubeconfig
WORKDIR /

COPY --from=builder /workspace/main ./server


ENTRYPOINT ["/server"]
