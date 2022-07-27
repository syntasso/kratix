FROM alpine:3.15.5

# This docker image is run in the xaas-request-pipeline-stage-X container inside the promise-controller.
# This container mounts 3 dirs (/input, /output, /metadata) which will overwrite any existing data at those paths.
# Instead, we will add our files to transfer dirs at build time, and moving the to the kubernetes mounted dirs at runtime.
ADD postgres-resource-request.yaml /transfer-input/postgres-resource-request.yaml
ADD knative-resource-request.yaml /transfer-input/knative-resource-request.yaml

# To debug: 
#  docker run -v `pwd`/input/:/input -v `pwd`/output/:/output syntasso/kustomize-redis
CMD [ "sh", "-c", "cp /transfer-input/* /output" ]
