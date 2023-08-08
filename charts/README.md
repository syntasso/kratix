# Kratix Charts

This directory contains two charts which you can use to install and operate
Kratix in either a single Kubernetes cluster, or across a multi-cluster setup.

To understand where to use each chart, you must understand a bit about the
way Kratix [schedules resources](https://kratix.io/docs/main/reference/multicluster-management)
across destinations.

## `kratix` core installation

Whether you plan to schedule all work to a single cluster, or spread across
multiple, you will need to use the `kratix` chart.

> **Note**
> The `kratix` chart installs the Kratix framework. If you are running a
> multi-cluster setup, this is typically installed on a `platform` or `admin`
> style cluster.


Using Kratix depends on access to a GitOps [state store](https://github.com/open-gitops/documents/blob/v0.1.0/PRINCIPLES.md#state-store).
It is suggested to register a Git repository or public cloud bucket, however
for a quick start, you can install and configure a local MinIO or other
cluster storage.

## `kratix-destination`

Kratix maintains a decoupled architecture which means that it does not
ever communicate with destination infrastructure directly. It is up to the
platform team to decide how to apply the Kratix documents once scheduled
to the correct state store.

In other words, Kratix does not take an opinion
on how your platform reconciles work to additional infrastructure.
However, we do provide this chart as a way to quickly install the CNCF
GitOps project [Flux](https://fluxcd.io/) and use the same state store
configuration options as used in the `kratix` chart.

## Common examples

* If you are running a single cluster setup, you will need to install both
charts on your cluster referring to the same state store configuration.

* If you are using any [compound promises](https://kratix.io/docs/main/guides/compound-promises),
you will also need to install both charts on the same cluster referring to
the same state store configuration.

* If you are running a multi-cluster setup, you will need to install the
`kratix` chart on your "platform" cluster and the `kratix-destination`
chart on all additional clusters, each with a unique state store
configuration.
