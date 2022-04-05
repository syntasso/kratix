FROM golang:1.17 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
COPY api/ api/ 
COPY work-creator/ work-creator/ 
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Build work-creator binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o work-creator work-creator/pipeline/cmd/main.go

# Use bitnami/kubectl to get ./kubeconfig 
FROM bitnami/kubectl:1.23.5
WORKDIR /
COPY --from=builder /workspace/work-creator/main ./work-creator
USER 65532:65532

ENTRYPOINT []
CMD []