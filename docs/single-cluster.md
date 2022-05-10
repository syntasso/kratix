# Quick Start

One of the most powerful features of Kratix is its ability to handle requests for resources, and deploy them to a remote specific cluster. The main [README.md](../README.md) outlines how to get started in a multi-cluster environment.

Follow this guide if you wish to _quick start_ to see Kratix work on a single cluster.

## Kratix: Single Cluster Deploy

### Prerequisites
- Connectivity and credentials to a k8s cluster (or one created with KinD, MiniKube, public cloud, etc)
- Install Kubectl. See [the install guide](https://kubernetes.io/docs/tasks/tools/#kubectl). Tested on 1.16.13 and 1.21.2.

### Clone Kratix
* `git clone git@github.com:Syntasso/kratix.git`

### Setup Platform Cluster and Install Kratix

This will create our platform cluster and install Kratix. We'll also install Minio to power our GitOps pipelines to the worker clusters (if we were multi-cluster). For production installations, Git or S3 can easily be used instead, depending on your preference.

* `kubectl apply -f distribution/kratix.yaml`
* `kubectl apply -f hack/platform/minio-install.yaml`

The Kratix API should now be available.

* `kubectl get crds`

```
NAME                                     CREATED AT
clusters.platform.kratix.io   2022-05-10T11:10:57Z
promises.platform.kratix.io   2022-05-10T11:10:57Z
works.platform.kratix.io      2022-05-10T11:10:57Z
```

### Setup the Gitops toolkit

This stage would typically be setup on a Worker cluster.  

* `kubectl apply -f config/samples/platform_v1alpha1_worker_cluster.yaml #register the cluster`
* `kubectl apply -f hack/worker/gitops-tk-install.yaml`
* `kubectl apply -f hack/worker/gitops-tk-resources-single-cluster.yaml`

Once Flux is installed and running (this may take a few minutes), the Kratix resources should now be visible on the your cluster.

* `kubectl get ns kratix-worker-system`
```
NAME                   STATUS   AGE
kratix-worker-system   Active   4m2s
```

### Apply Postgres-as-a-Service Promise

Now we have Kratix available to power our platform API, we need to put it to good use. We should spend time with our SATs to understand their needs, combine those needs with the organisation's needs around security, governance, and compliance, and encode this knowledge in a Promise. For the purpose of this walkthrough let's install the provided Postgres-as-a-service Promise.

* `kubectl apply -f samples/postgres/postgres-promise.yaml`

We should now see that our platform cluster offers the ability to create Postgres instances.

* `kubectl get crds postgreses.example.promise.syntasso.io`

```
NAME                                     CREATED AT
postgreses.example.promise.syntasso.io   2021-09-03T12:02:20Z
```

### Review created Postgres cluster-scoped

If we examine the cluster, after our configuration has been applied (may take a few moments), we see that the cluster-level resources necessary to host Postgres instances (the operator) have been deployed. Also notice that there are currently zero Postgres instances.

* `kubectl get pods`
```
NAME                                 READY   STATUS    RESTARTS   AGE
postgres-operator-55b8549cff-s77q7   1/1     Running   0          51s
```

### Request a Postgres Instance 

We now assume the role of a member of a stream-aligned team, and request a Postgres server from the platform API.

* `kubectl apply -f samples/postgres/postgres-resource-request.yaml`

We can see the request on the platform cluster.

* `kubectl get postgreses.example.promise.syntasso.io`
```
NAME                   AGE
acid-minimal-cluster   27s
```

### Review created Postgres Instance

Once the GitOps Toolkit has applied the new configuration (this may take a few moments), the Postgres instance will be created.

* `kubectl get pods`
```
NAME                                 READY   STATUS    RESTARTS   AGE
acid-minimal-cluster-0               1/1     Running   0          94s
acid-minimal-cluster-1               1/1     Running   0          58s
postgres-operator-55b8549cff-s77q7   1/1     Running   0          2m46s
```
