FROM "mikefarah/yq:4"
RUN [ "mkdir", "/tmp/transfer" ]

ADD execute-pipeline.sh execute-pipeline.sh
ADD jenkins_instance.yaml /tmp/transfer/jenkins_instance.yaml
ADD service_account.yaml /tmp/transfer/service_account.yaml

CMD [ "sh", "-c", "./execute-pipeline.sh"]
ENTRYPOINT []

