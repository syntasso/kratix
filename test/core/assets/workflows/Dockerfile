FROM "alpine"

RUN apk update && apk add --no-cache yq kubectl

ADD scripts/* /usr/bin/

RUN chmod +x /usr/bin/*

ENTRYPOINT []
