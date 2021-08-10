FROM "mikefarah/yq:4"
RUN [ "mkdir", "/tmp/transfer" ]

ADD execute-pipeline.sh execute-pipeline.sh
ADD minimal-postgres-manifest.yaml /tmp/transfer/minimal-postgres-manifest.yaml

CMD [ "sh", "-c", "./execute-pipeline.sh"]
ENTRYPOINT []
