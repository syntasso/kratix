# Getting Started -- the hard way!

In order to understand the power of internal platforms, let's build a demonstration platform using Kratix!

First, we're going to assume the role of a platform team member. We're going to create an internal platform for our stream-aligned teams (SATs, a.k.a. "application" or "development" teams). Our first step is to create a "platform" Kubernetes cluster to host our internal platform, and install Kratix on our platform cluster to power our platform API. We're now ready to add functionality to our platform.

Next, we will create a "worker" Kubernetes cluster to host the workloads delivered, X-as-a-Service, to our SATs. It is possible to add as many clusters as you wish to Kratix, and to dynamically create new clusters when desired, but right now we'll stick with one worker cluster for demonstration purposes. We'll also configure our worker cluster to join the platform cluster's Kratix topology. We're now ready to host workloads for our SATs.

Then we're going to add a sample Postgres Promise to our Kratix-powered platform API, so our SATs can request instances of Postgres "on demand" from our platform. The Postgres Promise encapsulates the knowledge of:
- The information the platform team needs to know from the SAT to create a Postgres instance on-demand (name, databases, etc).
- Which resources need to be present on the worker cluster to host instances (the Postgres operator).
- How to security check, scan, validate, and mutate etc. from the SAT's instance request to a set of per-instance Kubernetes resources to be applied on the worker cluster (a simple yaml transformation in this example).
The Promise enables the platform team to promise an organisationally-relevant Postgres service - or whichever services are of value in their platform - to the SATs, and to keep their promise.

Lastly, we're going to assume the role of a SAT member, make a request to the platform API for an instance of Postgres, and watch the instance get created in real time on the worker cluster. Postgres is now delivered X-as-a-service from the platform team to our SATs.


## Prerequisites
- A reasonably powerful computer. We test on:
    - Linux(Mint), i7, 16GB, KinD on native Docker.
    - Mac, i7, 32GB, KinD on a Docker Desktop VM(6 vCPU / 24GB).
- Install Kubernetes-in-Docker (KinD). See [the KinD quick start guide](https://kind.sigs.k8s.io/docs/user/quick-start/) to install KinD. Tested on 0.9.0 and 0.10.0. Use the [Kratix Quick Start](./single-cluster.md) for non-KinD installations.
    - Ensure no KinD clusters are currently running. `kind get clusters` should return "No kind clusters found."
- Install Kubectl. See [the install guide](https://kubernetes.io/docs/tasks/tools/#kubectl). Tested on 1.16.13 and 1.21.2.

## Part I: Kratix Multi-Cluster Install

### Clone Kratix

```bash
git clone https://github.com/syntasso/kratix.git
cd kratix
```

### Setup Platform Cluster and Install Kratix

This will create our platform cluster and install Kratix. We'll also install Minio to power our GitOps pipelines to the worker clusters. For production installations, Git or S3 can easily be used instead, depending on your preference.

```bash
# Create the platform cluster using kind. It also switch the kubectl context to the newly created cluster.
kind create cluster --name platform
```

You can validate if your cluster was created successfully by running the following commands:
```bash
kubectl cluster-info
kubectl get nodes --output wide
```

To install Kratix, run:
```
kubectl apply --filename distribution/kratix.yaml
```

The Kratix API should now be available. You can validate that by running:

```bash
kubectl --context kind-platform get crds
# NAME                                    CREATED AT
# clusters.platform.kratix.io   2022-05-10T11:10:57Z
# promises.platform.kratix.io   2022-05-10T11:10:57Z
# works.platform.kratix.io      2022-05-10T11:10:57Z

kubectl --context kind-platform get pods --namespace kratix-platform-system
# NAME                                                  READY   STATUS    RESTARTS   AGE
# kratix-platform-controller-manager-765c78b5dd-jq8zg   2/2     Running   0          107s
```

In a nutshell, Kratix spins up a controller manager that is able to dynamically generate other Kubernetes controllers. You can see the pod running and access its logs by running:

```bash
kubectl --context kind-platform get pods --namespace kratix-platform-system
# NAME                                                  READY   STATUS    RESTARTS   AGE
# kratix-platform-controller-manager-765c78b5dd-jq8zg   2/2     Running   0          4m21s

kubectl logs --namespace kratix-platform-system kratix-platform-controller-manager-85d94795bd-sgw7s manager
```

### Install Minio

The communication mechanism between the clusters are via the GitOps toolkit (Flux). The platform cluster writes resource definitions into the GitOps toolkit, which we use to continuosly synchronise the clusters. For our example, we will use Minio, but this could be replaced by other storage mechanisms that can speak either S3 or Git.

To install Minio, run:
```
kubectl apply --filename hack/platform/minio-install.yaml
```

Minio should now be up and running on your platform cluster, as a pod in the `kratix-platform-system` namespace. You can now access Minio on http://localhost:9000 by running the following commands:

```bash
# Get the name of the minio pod
minio_pod=$(kubectl --context kind-platform --namespace kratix-platform-system  get pods --selector run=minio --output custom-columns=:metadata.name --no-headers)

# Port-forward the service
kubectl --context kind-platform --namespace kratix-platform-system port-forward ${minio_pod} 8080:9000
```

Alternativaly, you can use the [Minio Client](https://docs.min.io/docs/minio-client-quickstart-guide.html) to navigate the buckets.

Minio's Access Key and Secret key can be obtained by:

```bash
# Access Key
> kubectl --namespace flux-system --context kind-worker get secret minio-credentials --output 'jsonpath={.data.accesskey}' | base64 --decode

# Secret Key
> kubectl --namespace flux-system --context kind-worker get secret minio-credentials --output 'jsonpath={.data.secretkey}' | base64 --decode
```

### Setup Worker Cluster

This will create a cluster for running the _X-as-a-service_ workloads, and install the GitOps Toolkit components to continuously converge the worker cluster on the desired state.

```bash
# Creates a new cluster with kind. It also switch the kubectl context to the newly created cluster.
kind create cluster --name worker

# Register the worker cluster with the platform cluster
kubectl --context kind-platform apply --filename config/samples/platform_v1alpha1_worker_cluster.yaml
```

Have a look in Minio now. There should be two buckets for the worker cluster: one for CRDs and one for resources:

```bash
mc ls kind
# [2022-05-25 15:44:24 BST]     0B worker-cluster-1-kratix-crds/
# [2022-05-25 15:44:25 BST]     0B worker-cluster-1-kratix-resources/

mc ls --recursive kind
# [2022-05-25 16:07:38 BST]   116B STANDARD worker-cluster-1-kratix-crds/kratix-crds.yaml
# [2022-05-25 16:07:50 BST]   158B STANDARD worker-cluster-1-kratix-resources/kratix-resources.yaml
```

Next, install the GitOps toolkit:

```
kubectl --context kind-worker apply --filename hack/worker/gitops-tk-install.yaml
kubectl --context kind-worker apply --filename hack/worker/gitops-tk-resources.yaml
```

You can run the following commands to validate whether the toolkit was successfully installed:

```bash
# Check if the pods are running
kubectl --context kind-worker get pods --namespace flux-system

# Verify Flux
kubectl --context kind-worker get --namespace flux-system buckets.source.toolkit.fluxcd.io kratix-workload-crds # and  kratix-workload-resources

# Verify the kustomizations
kubectl --context kind-worker get --namespace flux-system kustomizations.kustomize.toolkit.fluxcd.io kratix-workload-crds # and kratix-workload-resources
```

Once Flux is installed and running (this may take a few minutes), the platform should be ready to accept new promises. You can validade the end-to-end installation by running the following commands:

```bash
kubectl --context kind-worker get namespace kratix-worker-system
# NAME                   STATUS   AGE
# kratix-worker-system   Active   4m2s

kubectl --context kind-platform get clusters.platform.kratix.io
# NAME               AGE
# worker-cluster-1   4m
```

By the now, your setup looks like the following diagram:

![Getting Started Step One](./images/getting-started-step-1.png)

## Part II: Install a Postgres-as-a-Service Promise on the Platform Cluster


### Apply the Postgres promise

Now we have Kratix available to power our platform API, we need to put it to good use. We should spend time with our SATs to understand their needs, combine those needs with the organisation's needs around security, governance, and compliance, and encode this knowledge in a Promise. For the purpose of this walkthrough let's install the provided Postgres-as-a-service Promise.

```bash
kubectl --context kind-platform apply --filename samples/postgres/postgres-promise.yaml
```

There are three parts to a promise: the CRDs, the pipeline (injected into a dynamic controller), and the worker cluster resources. You can check these parts by running the following commands:

```bash
# CRDS
kubectl --context kind-platform get crds
# NAME                                     CREATED AT
# postgreses.example.promise.syntasso.io   2022-05-31T18:31:20Z

# Pipeline
# Look for new controller starting to listen to the Promise's CRD on the manager logs
kubectl --context kind-platform logs --namespace kratix-platform-system --container manager [kratix-pod-name]

# Worker cluster resources: check the running pods, you should (eventually) see the operator
# Also check Minio and whether Flux has applied the changes
kubectl --context kind-worker get pods
# NAME                                 READY   STATUS    RESTARTS   AGE
# postgres-operator-55b8549cff-s77q7   1/1     Running   0          51s
```

By the now, your setup looks like the following diagram:

![Getting Started Step Two](./images/getting-started-step-2.png)

## Part III: Request a Postgres Instance

We now assume the role of a member of a stream-aligned team, and request a Postgres server from the platform API.

```bash
kubectl --context kind-platform apply --filename samples/postgres/postgres-resource-request.yaml
```
We can see the request on the platform cluster.

```bash
kubectl get postgreses.example.promise.syntasso.io
# NAME                   AGE
# acid-minimal-cluster   27s
```

You can also check the manager logs for a line containing:

```
... Dynamically Reconciling: acid-minimal-cluster
... Creating Pipeline for Promise resource request: ha-postgres-promise-default-<...>. The pipeline will now execute...
```

The pipeline runs as a pod:

```bash
kubectl --context kind-platform get pods
# NAME                                                 READY   STATUS      RESTARTS   AGE
# request-pipeline-ha-postgres-promise-default-53903   0/1     Completed   0          5m50s
```

You can access the pipeline logs by running:
```bash
kubectl --context kind-platform logs request-pipeline-ha-postgres-promise-default-53903 xaas-request-pipeline-stage-1
# For the postgres pipeline, it will be empty if successfull
```

The output of the pipeline is a _Work_. At this point, you will find two _works_ on your platform cluster: one for the worker cluster resources (operator + CRD), one for the resource request (Postgres itself):
```bash
kubectl --context kind-platform get works
# NAME                                                       AGE
# ha-postgres-promise-default                                12m
# ha-postgres-promise-default-default-acid-minimal-cluster   2m47s
```

A new _work_ triggers the Scheduler to allocate that work. You can see the scheduling decision in the manager logs. Look for a line that has something like:

```
INFO    controllers.Scheduler   Adding Worker Cluster: worker-cluster-1
```

The Scheduler generates a _work placement_,  where you can verify to which cluster the _work_ was scheduled to:

```bash
kubectl --context kind-platform get workplacements.platform.kratix.io
kubectl --context kind-platform get workplacements.platform.kratix.io ha-postgres-promise-default.worker-cluster-1 -- output yaml
```

Finally, you can verify that the BucketWriter has written to the Minio buckets. You should see a line similar to the following in the manager logs:

```bash
INFO    controllers.BucketWriter        Creating Minio object 01-default-postgres-promise-default-default-acid-minimal-cluster-resources.yaml
```

You should find a new object on your Minio bucket at this point:

```bash
mc ls worker-cluster-1-kratix-resources
# ...
# [2022-05-31 18:15:31 BST] 1.7KiB worker-cluster-1-kratix-resources/01-default-ha-postgres-promise-default-resources.yaml
# ...
```

### Review created Postgres Instance on the Worker Cluster

Once the GitOps Toolkit has applied the new configuration to the worker cluster (this may take a few moments, check the `kustomizations`), the Postgres instance will be created on the worker cluster.

You can check the Postgres Operator logs with:

```bash
kubectl -context kind-worker logs postgres-operator-6c6dbd4459-5gqhf postgres-operator
```

You can also review the created instances with:

```bash
kubectl --context kind-worker get postgresqls.acid.zalan.do
# NAME                   TEAM   VERSION   PODS   VOLUME   CPU-REQUEST   MEMORY-REQUEST   AGE     STATUS
# acid-minimal-cluster   acid   13        2      1Gi                                     4m55s   Running

kubectl --context kind-worker get pods
# NAME                                 READY   STATUS    RESTARTS   AGE
# NAME                                 READY   STATUS    RESTARTS   AGE
# acid-minimal-cluster-0               1/1     Running   0          5m29s
# acid-minimal-cluster-1               1/1     Running   0          4m48s
# postgres-operator-6c6dbd4459-5gqhf   1/1     Running   0          15h
```

The following diagram illustrate what we have done:

![Getting Started Step Three](./images/getting-started-step-3.png)

## What have we learned?

We created an internal platform API, and a worker cluster to host workloads for our stream-aligned teams. We then decorated our platform API by Promising Postgres-as-a-service. Finally, we adopted the role of a stream-aligned team member and requested a Postgres instance from the platform. The Postgres instance was created on the worker cluster.

## Feedback

Please help to improve Kratix. Give feedback [via email](mailto:feedback@syntasso.io?subject=Kratix%20Feedback) or [google form](https://forms.gle/WVXwVRJsqVFkHfJ79). Alternatively, open an issue or pull request.

## Challenge
[Write your own Promise](./writing-a-promise.md), with a custom pipeline image, and share it with the world!
