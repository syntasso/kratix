FROM line/kubectl-kustomize:1.24.2-4.5.5
RUN [ "mkdir", "/transfer" ]
ADD patch.yaml /transfer/patch.yaml
ADD kustomization.yaml /transfer/kustomization.yaml

# To debug:
#  kubectl get database.postgresql.dev4devs.com postgres --namespace database -oyaml > input/object.yaml
#  docker run -v `pwd`/input/:/input -v `pwd`/output/:/output syntasso/kustomize-postgres
CMD [ "-c", "cp /transfer/* /kratix/input/; kustomize build /kratix/input/ > /kratix/output/output.yaml" ]
