#! /bin/bash
INPUT="/input/object.yaml"
NAME=$(grep 'name:' $INPUT | tail -n1 | awk '{ print $2}')
SERVICE_ACCOUNT=$(grep 'gcpServiceAccount:' $INPUT | tail -n1 | awk '{ print $2}')
PROJECT=$(grep 'gcpProject:' $INPUT | tail -n1 | awk '{ print $2}')
GCP_CREDS_SECRET_NAME=$(grep 'gcpSecretName:' $INPUT | tail -n1 | awk '{ print $2}')
MINIO_ENDPOINT=$(grep 'minioEndpoint:' $INPUT | tail -n1 | awk '{ print $2}')

kubectl crossplane install configuration salaboy/spicy-project-template-gcp:0.1.0

sleep 5

find worker-cluster-resource.yaml -type f -exec sed -i -e "s/<tbr-name>/${NAME//\//\\/}/g" {} +; 
find kratix-worker-cluster.yaml -type f -exec sed -i -e "s/<tbr-name>/${NAME//\//\\/}/g" {} +; 

kubectl apply -f  kratix-worker-cluster.yaml
kubectl apply -f  worker-cluster-resource.yaml

while true
do
  sleep 1
  output="$(kubectl get spicyprojecttemplategcps.fmtok8s.salaboy.com $NAME 2>&1)"
  echo $output
  [[ $output =~ "True" ]] && break
  echo "Worker Cluster is still creating in GCloud"
done

## Get GCloud Credentials so we can login to our GKE Cluster
kubectl get secret -n crossplane-system $GCP_CREDS_SECRET_NAME --template={{.data.creds}} | base64 -d >> gcloud-creds.json
gcloud auth activate-service-account $SERVICE_ACCOUNT --key-file=gcloud-creds.json
## Sets KubeConfig to new GKE Cluster
gcloud container clusters get-credentials cluster-$NAME --region us-central1 --project $PROJECT

find gitops-tk-resources.yaml -type f -exec sed -i -e "s/<tbr-minio-endpoint>/${MINIO_ENDPOINT//\//\\/}/g" {} +; 

## Make GKE Cluster a Kratix Worker Cluster
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/dev/hack/worker/gitops-tk-install.yaml
kubectl apply -f gitops-tk-resources.yaml 

## wait until Kratix Namespace is created on the Worker cluster
while true
do
  sleep 1
  output="$(kubectl get ns 2>&1)"
  echo $output
  [[ $output =~ "kratix-worker-system" ]] && break
  echo "Kratix is Still installing"
done

#Install Knative-Serving
kubectl config unset current-context

## Install Knative Promise
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/dev/samples/knative-serving/knative-serving-promise.yaml
sleep 5 # Don't judge us, this was for a public demo! 
## Ask for Knative Instance
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/dev/samples/knative-serving/knative-serving-resource-request.yaml