# SynPl FTW

## Installation 

### Preqs
* Install Kind

### Setup Platform Cluster
* `kind create cluster --name platform`
* `kubectl apply -f hack/platform/minio-install.yaml`
** Create a bucket called "synpl"
* `make deploy`

### Setup Worker Cluster
* `kind create cluster --name worker`
* `kubectl apply -f config/samples/redis/redis-crd.yaml`
* `kubectl apply -f hack/worker/flux-install.yaml`
* `kubectl apply -f hack/worker/flux-crs.yaml`

### Target Platform Cluster
* `k config use-context kind-platform`

### Optional: Modify the Promise so Penny gets a custom annotation
* `cd config/samples/redis/transformation-image/`
* `vim patch.yaml`
* `docker build . --tag syntasso/kustomize-redis`
* `docker push syntasso/kustomize-redis`
* `cd -`

### Create Redis Promise
* `kubectl apply -f config/samples/redis/redis-promise.yaml`

### Request a Redis
* `kubectl apply -f config/samples/redis/redis-resource-request.yaml`