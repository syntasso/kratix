FROM "alpine"

RUN apk update && apk add --no-cache yq curl

RUN curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.25.1/bin/linux/amd64/kubectl
RUN chmod u+x kubectl && mv kubectl /bin/kubectl

ADD execute-pipeline /execute-pipeline
RUN chmod +x /execute-pipeline

CMD [ "/execute-pipeline"]
