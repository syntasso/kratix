FROM "alpine"

RUN apk update && apk add --no-cache yq curl

RUN curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.25.1/bin/linux/amd64/kubectl
RUN chmod u+x kubectl && mv kubectl /bin/kubectl

ARG CONTAINER_INDEX
ENV CONTAINER_INDEX=${CONTAINER_INDEX}

CMD [ "sh", "-c", "eval \"$(yq .spec.container${CONTAINER_INDEX}Cmd /kratix/input/object.yaml)\"" ]
ENTRYPOINT []
