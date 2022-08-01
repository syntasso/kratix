FROM line/kubectl-kustomize:1.24.2-4.5.5
RUN [ "mkdir", "/transfer" ]

ADD jenkins_instance.yaml /transfer/jenkins_instance.yaml
ADD kustomization.yaml /transfer/kustomization.yaml

CMD [ "-c", "cp -r /transfer/* /input/; kustomize build /input > /output/output.yaml" ]
