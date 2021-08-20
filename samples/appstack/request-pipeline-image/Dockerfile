FROM "mikefarah/yq:4"
RUN [ "mkdir", "/tmp/transfer" ]

ADD execute-pipeline.sh execute-pipeline.sh
ADD builder.yaml /tmp/transfer/builder.yaml
ADD image-build.yaml /tmp/transfer/image-build.yaml
ADD app.yaml /tmp/transfer/app.yaml
ADD sa.yaml /tmp/transfer/sa.yaml
ADD docker-secret.yaml /tmp/transfer/docker-secret.yaml

CMD [ "sh", "-c", "./execute-pipeline.sh"]
ENTRYPOINT []
