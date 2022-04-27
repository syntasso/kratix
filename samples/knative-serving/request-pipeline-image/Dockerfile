FROM "mikefarah/yq:4"
RUN [ "mkdir", "/tmp/transfer" ]

ADD execute-pipeline.sh execute-pipeline.sh
ADD kourier.yaml /tmp/transfer/kourier.yaml
ADD serving-core.yaml /tmp/transfer/serving-core.yaml
ADD serving-default-domain.yaml /tmp/transfer/serving-default-domain.yaml

CMD [ "sh", "-c", "./execute-pipeline.sh"]
ENTRYPOINT []
