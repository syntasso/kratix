# How Kratix Compares to Other Technologies in the Ecosystem?

## Kubernetes Operators

Kubernetes Operators work hand-in-hand with Kratix. In nearly all cases Operators will need to be deployed in the `workerClusterResources` part of a Promise, to worker clusters, to ensure worker clusters are able to manage requested instances.

## Helm

Kratix committers are currently working on an easy to use Helm Promise template so it will be trivial to offer Helm Charts as Promises. Watch this space!

## Crossplane

Crossplane is an ideal candidate for a Promise, and works well with Kratix. Crossplane's complexity can be hidden from stream-aligned teams by platform teams, and IaaS(AWS, Google, MS)-specific clusters, with bespoke Crossplane implementations, used with a Kratix-powered platform. A sample Crossplane Promise is under development.

## OLM

RedHat's Operator Lifecycle Manager(OLM) is an ideal candidate for a Promise. OLM is a single-cluster solution, of high complexity, and ideally suited for Kratix's multi-cluster GitOps orchestration, combined with the codification of the roles for the platform and stream-aligned teams. OLM is an excellent way to manage the operators used by Kratix.

## OCM

Open Cluster Management(OCM) shares many ideas with Kratix, in particular the "Work" resource across multiple clusters, but takes a different direction in some areas. OCM philosophically appears to attempt to treat multiple clusters as one big cluster, with tight coupling between managed clusters via the klusterlet agent. Kratix decouples managed clusters, orchestrating the distributed platform via the GitOps Toolkit, enabling greater scale and resiliency of the platform as a whole. The enables the platform team to readily debug, audit, and control what's being deployed to managed clusters. This also enables the platform team to pause updates from the platform cluster to worker clusters, or add additional resources to the GitOps repositories directly.