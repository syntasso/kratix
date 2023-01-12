FROM "alpine"

RUN [ "mkdir", "/tmp/transfer" ]
RUN apk update && apk add --no-cache yq curl

RUN curl -sL https://github.com/knative/client/releases/download/knative-v1.8.1/kn-linux-amd64 -o kn && chmod +x kn && mv kn /usr/local/bin/kn

ADD execute-pipeline execute-pipeline

CMD [ "sh", "-c", "./execute-pipeline" ]
ENTRYPOINT []
