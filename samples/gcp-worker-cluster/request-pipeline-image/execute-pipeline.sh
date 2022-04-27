#! /bin/bash
INPUT="/input/object.yaml"
SERVICE_ACCOUNT=$(grep 'gcpServiceAccount:' $INPUT | tail -n1 | awk '{ print $2}')
PROJECT=$(grep 'gcpProject:' $INPUT | tail -n1 | awk '{ print $2}')
GCP_CREDS_SECRET_NAME=$(grep 'gcpSecretName:' $INPUT | tail -n1 | awk '{ print $2}')
MINIO_ENDPOINT=$(grep 'minioEndpoint:' $INPUT | tail -n1 | awk '{ print $2}')

kubectl crossplane install configuration salaboy/worker-cluster-gcp:0.1.0

sleep 5

kubectl apply -f  worker-cluster.yaml
kubectl apply -f  worker-cluster-resource.yaml

while true
do
  sleep 1
  output="$(kubectl get workerclusters.fmtok8s.salaboy.com my-worker-cluster 2>&1)"
  echo $output
  [[ $output =~ "True" ]] && break
  echo "Worker Cluster is still creating in GCloud"
done

## Install Knative Promise
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/main/samples/knative-serving/knative-serving-promise.yaml
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/main/samples/knative-serving/jenkins-resource-request.yaml

## Get GCloud Credentials so we can login to our GKE Cluster
kubectl get secret -n crossplane-system $GCP_CREDS_SECRET_NAME --template={{.data.creds}} | base64 -d >> gcloud-creds.json
gcloud auth activate-service-account $SERVICE_ACCOUNT --key-file=gcloud-creds.json
## Sets KubeConfig to new GKE Cluster
gcloud container clusters get-credentials hc-my-worker-cluster --region us-central1 --project $PROJECT

find gitops-tk-resources.yaml -type f -exec sed -i -e "s/<tbr-minio-endpoint>/${MINIO_ENDPOINT//\//\\/}/g" {} +; 

## Make GKE Cluster a Kratix Worker Cluster
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/dev/hack/worker/gitops-tk-install.yaml
kubectl apply -f gitops-tk-resources.yaml 