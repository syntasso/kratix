FROM lyft/kustomizer:v3.3.0
RUN [ "mkdir", "/transfer" ]

ADD kustomization.yaml /transfer/kustomization.yaml
ADD minimal-postgres-manifest.yaml /transfer/minimal-postgres-manifest.yaml

CMD [ "sh", "-c", "cp -r /transfer/* /input/; kustomize build /input > /output/output.yaml" ]