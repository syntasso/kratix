FROM lyft/kustomizer:v3.3.0
RUN [ "mkdir", "/transfer" ]

ADD kustomization.yaml /transfer/kustomization.yaml
ADD minimal-postgres-manifest.yaml /transfer/minimal-postgres-manifest.yaml

CMD [ "sh", "-c", \
      "cp -r /transfer/* /input/; \
       name=$(grep 'name:' /input/object.yaml |  awk '{print $2}') &&  sed -i 's/TBD/'${name}'/g' /input/minimal-postgres-manifest.yaml; \
       name=$(grep 'name:' /input/object.yaml |  awk '{print $2}') &&  sed -i 's/TBD/'${name}'/g' /input/kustomization.yaml; \
       kustomize build /input > /output/output.yaml" \
    ]