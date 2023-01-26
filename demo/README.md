# Demo playbook
## Prep env
Make sure your logged into lpass before running the setup script. If you want
to run in (low-internet mode ensure you've saved the images locally)[saving-the-images-for-low-internet]):
```
./scripts/setup --preload-images
```

otherwise run the following to setup normally:
```
./scripts/setup
```

## Running the demo
Change into the `app-as-a-service` directory for the demo.
```
cd app-as-a-service/
```

### Installing
To install:
```
kubectl create -f promise.yaml
```

Show promises are installed:
```
kubectl get promises
```

Show the App CRD is installed:
```
kubectl get crds | grep app
```

(Optional) Show the installed worker cluster resources:
```
kubectl --context kind-worker get pods
```

### Making a simple resource request

Show what a resource request looks like (using [bat for pretty output](https://github.com/sharkdp/bat):
```
bat resource-request.yaml
```

Make a resource request:
```
kubectl apply -f resource-request.yaml
```

Show pipelines firing:
```
kubectl get pods
```

*Switch to worker cluster*
```
kubectx kind-worker
```

Watch pods coming up
```
kubectl get pods
```

Once the Redis, Postgres and TODO app are running start a portforward:
```
kubectl --namespace knative-serving port-forward svc/kourier 8081:80
```

Show the app working by going to http://todo.default.local.gd:8081


### Making a more complicated resource request (PCI compliant)
| :warning: WARNING          |
|:---------------------------|
| Switch back to platform cluster and delete the previous resource request first |

Show what the resource request looks like and talk though how `containsCreditCardData` encapsulates orgs business logic:
```
bat resource-request-cc.yaml
```

Show what clusters we have with labels:
```
kubectl get clusters --show-labels
```

Make a resource request:
```
kubectl apply -f resource-request.yaml
```

Show the pipelines run
```
kubectl get pods
```

Show that nothing comes up on the worker:
```
kubectl --context kind-worker get pods
```

Label the worker cluster
```
kubectl label cluster worker-cluster-1 pci=true
```

Show that finally the pods comes up on the worker:
```
kubectl --context kind-worker get pods
```

| :warning: WARNING          |
|:---------------------------|
| due to a bug with the knative operator you cannot port-forward and show
the app on the 2nd run through. something about deleting the first resource
request prevents following resource requests from working |


---

## Saving the images for low internet

If you intend to to run this in a low-internet environment we recommend pulling
and saving most of the required images before hand (see last section for why this isn't fully offline by default).

To generate a list of all the images required follow the below instructions, running
all the commands from within the `demo` directory.

### Generating the `demo-image-list`
1. run demo from start to finish with full internet access. This will generate all necessary running pods.
  * `./scripts/setup`
  * `kubectl create -f app-as-a-service/promise.yaml`
  * `kubectl apply -f app-as-a-service/resource-request.yaml`
  * Follow the app-as-a-service readme to run the demo app (this is necessary since the app only creates pods on demand)
2. run the following set of commands:
  ```
  kubectl get pods --context kind-worker --all-namespaces -o jsonpath="{.items[*].spec.containers[*].image}" |\
    tr -s '[[:space:]]' '\n' > /tmp/demo-image-list
  echo >>  /tmp/demo-image-list
  kubectl get pods --context kind-worker --all-namespaces -o jsonpath="{.items[*].spec.initContainers[*].image}" |\
    tr -s '[[:space:]]' '\n' >>  /tmp/demo-image-list
  echo >>  /tmp/demo-image-list
  kubectl get pods --context kind-platform --all-namespaces -o jsonpath="{.items[*].spec.containers[*].image}" |\
    tr -s '[[:space:]]' '\n' >> /tmp/demo-image-list
  echo >>  /tmp/demo-image-list
  kubectl get pods --context kind-platform --all-namespaces -o jsonpath="{.items[*].spec.initContainers[*].image}" |\
    tr -s '[[:space:]]' '\n' >>  /tmp/demo-image-list

  cat /tmp/demo-image-list | sort | uniq | grep -v "syntasso/kratix-platform" |  grep -v "knative-release" | grep -v "sample-todo-app" > demo-image-list
  ```

### Saving the images
Now you have a `demo-image-list` file you can run the following:

```
mkdir -p cached-images
../scripts/download-images $PWD/cached-images/ $PWD/demo-image-list
```

This will save a tar of all the images.

### Why doesn't this work completely offline?
We can't get the knative GCR and TODO-app images to successfully load. Context: https://github.com/kubernetes-sigs/kind/issues/2394#issuecomment-1397720494

The current workaround if you need to go truly offline is to run the above setup script to get the environment
setup, and then manually load all the images with a digest (all the knatives for example) in the `demo-image-list`:
```
cat demo-image-list| grep @ | xargs -I{} docker exec platform-control-plane crictl pull {}
cat demo-image-list| grep @ | xargs -I{} docker exec worker-control-plane crictl pull {}
```

In addition, the following two pods can not start without internet access:
* knative-operator-579648cc6b-sczgr
* operator-webhook-7df689586-k22mr

Example error is, however this image can not be pulled:
```
  Warning  Failed     10s (x3 over 26s)  kubelet            Error: failed to get image from containerd "sha256:1e7e67348c2fce975e89c1670537a68ff1e1131467d94f42d8d8fb8f9a15cb4b": image "docker.io/library/import-2023-01-23@sha256:06af9cb3e0ddf9bf4d01feda64372a694e2a581419bf61518a2b98f6f80c26e6": not found
```
