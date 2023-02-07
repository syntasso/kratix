# Saving demo images for low internet

If you intend to to run this in a low-internet environment we recommend pulling
and saving most of the required images before hand (see last section for why this isn't fully offline by default).

To generate a list of all the images required follow the below instructions,

### Generating the `demo-image-list`

> Run all of these commands from within the `demo` directory.

#### 1. Run the demo start to finish

Run demo from start to finish with full internet access. This will generate all necessary running pods.

- `./scripts/setup`: Create clusters, install Kratix
- `kubectl create -f app-as-a-service/promise.yaml`: Install AaaS Promise
- `kubectl apply -f app-as-a-service/resource-request.yaml`: Install Resource Request
- Run the demo app so that all the required pods get created.
  - `./scripts/wait-and-open-browser-when-app-ready`: Run the script that automatically waits for resources to be ready, sets up the port forward, and opens the app in the browser.
  - Alternatively, follow the [AaaS Readme](https://github.com/syntasso/kratix/tree/main/demo/app-as-a-service) to run the demo app.

#### 2. Create list of image names

Generate the list of the names of images you will need for the demo:

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

#### 3. Save the images locally

Now you have a `demo-image-list` file, save a tar of all the images:

```
mkdir -p cached-images
../scripts/download-images $PWD/cached-images/ $PWD/demo-image-list
```

You now have a local set of (almost) all images required for the demo.

#### Why doesn't this work completely offline?

We can't get the knative GCR and TODO-app images to successfully load. Context: https://github.com/kubernetes-sigs/kind/issues/2394#issuecomment-1397720494

The current workaround if you need to go truly offline is to run the above setup script to get the environment
setup, and then manually load all the images with a digest (all the knatives for example) in the `demo-image-list`:

```
cat demo-image-list| grep @ | xargs -I{} docker exec platform-control-plane crictl pull {}
cat demo-image-list| grep @ | xargs -I{} docker exec worker-control-plane crictl pull {}
```

In addition, the following two pods can not start without internet access:

- knative-operator-579648cc6b-sczgr
- operator-webhook-7df689586-k22mr

Example error is, however this image can not be pulled:

```
  Warning  Failed     10s (x3 over 26s)  kubelet            Error: failed to get image from containerd "sha256:1e7e67348c2fce975e89c1670537a68ff1e1131467d94f42d8d8fb8f9a15cb4b": image "docker.io/library/import-2023-01-23@sha256:06af9cb3e0ddf9bf4d01feda64372a694e2a581419bf61518a2b98f6f80c26e6": not found
```
