# make sure yq is available to the pipeline script
FROM "mikefarah/yq:4"

ADD execute-pipeline.sh execute-pipeline.sh

# add any assests required by your pipeline script
ADD asset.yaml asset.yaml

CMD [ "sh", "-c", "./execute-pipeline.sh"]
ENTRYPOINT []

