FROM line/kubectl-kustomize:1.24.2-4.5.5

# This docker image is run in the xaas-request-pipeline-stage-X container inside the promise-controller.
# This container mounts 3 dirs (/input, /output, /metadata) which will overwrite any existing data at those paths.
# Instead, we will add our files to transfer dirs at build time, and moving the to the kubernetes mounted dirs at runtime.
ADD scheduling.yaml /transfer-metadata/scheduling.yaml
ADD kustomization.yaml /transfer-input/kustomization.yaml

# To debug: 
#  kubectl get redis.redis.redis.opstreelabs.in opstree-redis --namespace default -oyaml > input/object.yaml
#  docker run -v `pwd`/input/:/input -v `pwd`/output/:/output syntasso/kustomize-redis
CMD [ "-c", "cp /transfer-metadata/* /metadata/; cp /transfer-input/* /input/; kustomize build /input/ > /output/output.yaml" ]
