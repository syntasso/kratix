FROM --platform=$TARGETPLATFORM ghcr.io/syntasso/kratix-pipeline-utility:v0.0.1

LABEL org.opencontainers.image.authors "kratix@syntasso.io"
LABEL org.opencontainers.image.source https://github.com/syntasso/kratix

RUN apk update && apk add --no-cache vim nano

ADD execute-pipeline.bash execute-pipeline.bash

CMD [ "sh", "-c", "./execute-pipeline.bash" ]
ENTRYPOINT []
