FROM alpine

LABEL org.opencontainers.image.authors="kratix@syntasso.io"
LABEL org.opencontainers.image.source=https://github.com/syntasso/kratix

RUN [ "mkdir", "/tmp/transfer" ]
RUN apk update && apk add --no-cache \
	bash yq curl git \
	pwgen bash openssl wget bind-tools \
	github-cli unzip fortune ruby file

RUN curl https://releases.hashicorp.com/terraform/1.7.1/terraform_1.7.1_linux_amd64.zip -o terraform.zip && \
    unzip terraform.zip && \
    mv terraform /usr/local/bin/terraform && \
    rm terraform.zip

ARG TARGETOS
ARG TARGETARCH
RUN curl -LO https://dl.k8s.io/v1.33.2/kubernetes-client-${TARGETOS}-${TARGETARCH}.tar.gz && \
    tar -xzf kubernetes-client-${TARGETOS}-${TARGETARCH}.tar.gz && \
    mv kubernetes/client/bin/kubectl /bin/kubectl-cli && \
    chmod 755 /bin/kubectl-cli && \
    rm -rf kubernetes-client-linux-amd64.tar.gz kubernetes
COPY kubectl-wrapper.sh /bin/kubectl
RUN chmod 755 /bin/kubectl

RUN file /bin/kubectl && /bin/kubectl version --client

RUN curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 \
    && chmod +x get_helm.sh && ./get_helm.sh
