FROM line/kubectl-kustomize:1.24.2-4.5.5
RUN [ "mkdir", "/transfer" ]

ADD bases /transfer/bases/
ADD overlays/ /transfer/overlays/

CMD [ "-c", \
      "target=`grep -A3 'envType:' /input/object.yaml | tail -n1 | awk '{ print $2}'`; \
      cp -r /transfer/* /input/; kustomize build /input/overlays/${target} > /output/output.yaml" \
    ]
