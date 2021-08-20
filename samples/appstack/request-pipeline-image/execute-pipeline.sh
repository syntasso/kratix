#!/bin/sh 

applicationName=$(yq eval '.spec.applicationName' /input/object.yaml) ; \
gitRepo=$(yq eval '.spec.gitRepo' /input/object.yaml) ; \
appImageTag=$(yq eval '.spec.appImageTag' /input/object.yaml) ; \
builderImageTag=$(yq eval '.spec.builderImageTag' /input/object.yaml) ; \
dockerConfigJson=$(yq eval '.spec.dockerConfigJson' /input/object.yaml) ; \
find /tmp/transfer -type f -exec sed -i \
  -e "s/<tbr-gitRepo>/${gitRepo//\//\\/}/g" \
  -e "s/<tbr-appImageTag>/${appImageTag//\//\\/}/g" \
  -e "s/<tbr-builderImageTag>/${builderImageTag//\//\\/}/g" \
  -e "s/<tbr-applicationName>/${applicationName//\//\\/}/g" \
  -e "s/<tbr-dockerConfigJson>/${dockerConfigJson//\//\\/}/g" \
  {} \;  

cp /tmp/transfer/* /output/