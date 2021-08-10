FROM lyft/kustomizer:v3.3.0
RUN [ "mkdir", "/transfer" ]

ADD jenkins_instance.yaml /transfer/jenkins_instance.yaml
ADD kustomization.yaml /transfer/kustomization.yaml

CMD [ "sh", "-c", \
      "cp -r /transfer/* /input/; kustomize build /input > /output/output.yaml" \
    ]
