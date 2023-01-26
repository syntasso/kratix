# Low internet demo run

## Reqs

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

### Setting up an env with the saved images
To setup an environment ready for the demo using the saved images:
```
./scripts/setup
```

This will create the clusters, loaded with the images.

### Why doesn't this work completely offline?
We can't get the knative GCR images to successfully load. Context: https://github.com/kubernetes-sigs/kind/issues/2394#issuecomment-1397720494

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
