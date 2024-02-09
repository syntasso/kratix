FROM --platform=$TARGETPLATFORM "alpine"

LABEL org.opencontainers.image.authors "kratix@syntasso.io"
LABEL org.opencontainers.image.source https://github.com/syntasso/kratix

RUN [ "mkdir", "/tmp/transfer" ]
RUN apk update && apk add --no-cache \
	bash yq curl git \
	pwgen bash openssl wget bind-tools \
	github-cli unzip fortune ruby

RUN curl https://releases.hashicorp.com/terraform/1.7.1/terraform_1.7.1_linux_amd64.zip -o terraform.zip && \
    unzip terraform.zip && \
    mv terraform /usr/local/bin/terraform && \
    rm terraform.zip

RUN curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.25.1/bin/linux/amd64/kubectl
RUN chmod u+x kubectl && mv kubectl /bin/kubectl

RUN curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 \
    && chmod +x get_helm.sh && ./get_helm.sh

