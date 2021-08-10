FROM lyft/kustomizer:v3.3.0
RUN [ "mkdir", "/transfer" ]

ADD bases /transfer/bases/
ADD overlays/ /transfer/overlays/

CMD [ "sh", "-c", \
      "target=`grep -A3 'envType:' /input/object.yaml | tail -n1 | awk '{ print $2}'`; \
      cp -r /transfer/* /input/; kustomize build /input/overlays/${target} > /output/output.yaml" \
    ]
