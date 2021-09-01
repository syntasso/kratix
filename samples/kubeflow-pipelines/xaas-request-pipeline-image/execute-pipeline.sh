#!/bin/sh 

applicationName=$(yq eval '.metadata.name' /input/object.yaml) ; \
find /tmp/transfer -type f -exec sed -i \
  -e "s/<tbr-kubeflow-per-install-namespace>/kfp-${applicationName//\//\\/}/g" \
  {} \;  

cp /tmp/transfer/* /output/