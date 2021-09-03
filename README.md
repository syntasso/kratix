# Kratix

The modern digital organisation is populated with customer-focused teams delivering business value. These teams need to create differentiated products for their customers, but are too often dragged down into wrangling commodity infrastructure components. This is the "Platform Gap": the chasm each team must cross between their infrastructure and their meaningful work adding product value.

The Platform Gap represents an opportunity for an economomy of scope. If multiple teams are utilising similar infrastructure components, an internal platform should be created based on the discovery and understanding of those common needs, and it should serve to reduce cognitive load for its users by meeting those needs "as-a-service", i.e. on demand and self-service. The ongoing objective of the platform should be to make the customer-focused teams more productive, efficient, and secure.

How does an organisation create an internal platform? By creating, growing, and sustaining an internal platform team. [Team Topologies provides a vocabulary for us to describe the team structures and interaction modes relevant to the internal platform](https://teamtopologies.com/key-concepts).
- Stream-aligned teams must cross the platform gap between their infrastructure and their product value
- The platform team delivers a "Platform as a Product" to reduce the cognitive load, closing the platform gap, for the stream-aligned teams
- The platform team collaborates with stream-aligned teams to discover, understand, prioritise, and encapsulate platform gap solutions as "Promises".
- The platform team keeps their Promises, X-as-a-Service, via the Platform-as-a-Product.
These team topologies and interaction modes provide a template for the future of DevOps at scale.

The Platform-as-a-Product must be self-service, API-consumable, compliant, governed, and in most cases, bespoke and customised to the organisation. Off-the-shelf or generic public cloud platforms don't meet the unique needs of modern digital organisations. A curated blend of off-the-shelf, composed, and self-created services are required in any organisation operating at scale.

Kubernetes is the infrastructure foundation layer for modern platforms, but is not the platform itself. Multi-cluster, composable, extensible platforms are based on Kubernetes's constructs are required. Kubernetes "operators" will drive software deployments on individual clusters, but are a long way from a full digital platform. A higher-level framework, above operators, is necessary to drive multi-cluster multi-team platforms. Kratix is that framework.

Kratix has been created to enable platform teams to easily compose internal platforms that are relevant and valuable to their organisation, to utilise the best of the Kubernetes ecosystem, and to balance the concerns of delivery speed with operational excellence.

## Getting Started

In order to understand the power of internal platforms, let's build a demonstration platform using Kratix!

First, we're going to create a "platform" Kubernetes cluster to host our platform, and install Kratix on our platform cluster to power our platform API.

Next, we will create a "worker" Kubernetes cluster to host the workloads delivered, X-as-a-Service, to our users. It is possible to add as many clusters as you wish to Kratix, and to dynamically create new clusters when desired, but right now we'll stick with one worker cluster for demonstration purposes. We'll also configure our worker cluster to join the platform cluster's Kratix topology.

Then, we're going to add a sample PostGres Promise to our Kratix-powered platform API, so our users can request instances of PostGres "on demand" from our platform.

Lastly, we're going to make a user request to the platform API for an instance of Postgres, and watch the instance get created in real time on the worker cluster.

### Prequisites
- A reasonably powerful computer. We test on:
    - Linux(Mint), i7, 16GB, KinD on native Docker.
    - Mac, i7, 32GB, KinD on Docker Desktop VM.
- Install Kubernetes-in-Docker(KinD). See [the quick start guide](https://kind.sigs.k8s.io/docs/user/quick-start/). Tested on 0.9.0.
    - Ensure no KinD clusters are currently running. `kind get clusters` should return "No kind clusters found."
- Install Kubectl. See [the install guide](https://kubernetes.io/docs/tasks/tools/#kubectl). Tested on 1.16.13.

### Setup Platform Cluster and Install Kratix
* `kind create cluster --name platform`
* `kubectl apply -f distribution/kratix.yaml`
* `kubectl apply -f hack/platform/minio-install.yaml`
The Kratix API should now be available.
* `kubectl get crds`
NAME                                     CREATED AT
promises.platform.kratix.io              2021-09-03T11:59:16Z
works.platform.kratix.io                 2021-09-03T11:59:16Z

### Setup Worker Cluster
* `kind create cluster --name worker`
* `kubectl apply -f hack/worker/flux-install.yaml`
* `kubectl apply -f hack/worker/flux-crs.yaml`
Once Flux is installed and running (may take a few minutes), the Kratix resources should now be deployed to the worker cluster.
* `kubectl get ns kratix-worker-system`
NAME                   STATUS   AGE
kratix-worker-system   Active   4m2s

### Apply PostGres-as-a-Service Promise on the Platform Cluster
* `kubectl config use-context kind-platform`
* `kubectl apply -f samples/postgres/postgres-promise.yaml`
* `kubectl get crds postgreses.example.promise.syntasso.io`
NAME                                     CREATED AT
postgreses.example.promise.syntasso.io   2021-09-03T12:02:20Z

### Review created PostGres cluster-scoped resources on the Worker Cluster
* `kubectl config use-context kind-worker`
* `kubectl get pods`
NAME                                 READY   STATUS    RESTARTS   AGE
postgres-operator-55b8549cff-dkz8c   1/1     Running   0          11m

### Request a PostGres Instance on the Platform Cluster
* `kubectl config use-context kind-platform`
* `kubectl apply -f samples/postgres/postgres-resource-request.yaml`
* `kubectl get postgreses.example.promise.syntasso.io`
NAME                   AGE
acid-minimal-cluster   27s

### Review created PostGres Instance on the Worker Cluster
* `kubectl config use-context kind-worker`
* `kubectl get pods`
NAME                                 READY   STATUS    RESTARTS   AGE
acid-minimal-cluster-0               1/1     Running   0          43s
acid-minimal-cluster-1               1/1     Running   0          8s

## Where next?
- Try the Kpack-and-Knative Application Stack to see a Promise comprising multiple elements of underlying software.
- Try out the other Promise samples.
    - Machine Learning with Kubeflow Pipelines
    - Messaging with RabbitMQ
    - CI/CD with Jenkins
- Author your own Promises to extend your platform API with bespoke services for your organisation.
- **Give feedback on Kratix. Please, please, please!**
- **Work with Kratix's originators, Syntasso, to deliver your organisation's Platform-as-a-Product.**

## Known Issues
- Very large (in terms of bytes of yaml) Promises may fall foul of Kubernetes's annotations size limit when using `kubectl apply`. A workaround is to use `kubectl create`. We will address this in the future with dependency management between Promises.
- The demonstration installation relies upon the platform cluster running on 172.18.0.2 and making Minio available on port 31337 to the worker cluster. This works on default settings but may conflict with custom installations.
- The demonstration installation exercises the "happy path" only. Any functionality beyond setup, applying a Promise, and creating an instance, is untested.