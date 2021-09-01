FROM "mikefarah/yq:4"
RUN [ "mkdir", "/tmp/transfer" ]

ADD execute-pipeline.sh execute-pipeline.sh
ADD env-dev.yaml /tmp/transfer/env-dev.yaml

CMD [ "sh", "-c", "./execute-pipeline.sh"]
ENTRYPOINT []
