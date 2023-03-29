# Saving demo images for low internet

If you intend to to run this in a low-internet environment we recommend pulling
and saving all of the required images before hand.

To generate a list of all the images required follow the below instructions,

### Generating the `demo-image-list`

> Run all of these commands from within the `demo` directory.

#### 1. Run the demo start to finish

Run demo from start to finish with full internet access. This will generate all necessary running pods.

- `./scripts/setup`: Create clusters, install Kratix
- `kubectl create -f app-as-a-service/promise.yaml`: Install AaaS Promise
- `kubectl apply -f app-as-a-service/resource-request.yaml`: Install Resource Request
- Run the demo app so that all the required pods get created.
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

cat /tmp/demo-image-list | sort | uniq | grep -v "syntasso/kratix-platform" | grep -v "syntassodev/kratix-platform" > demo-image-list
```

#### 3. Save the images locally

Now you have a `demo-image-list` file, save a tar of all the images:

```
mkdir -p cached-images
../scripts/download-images $PWD/cached-images/ $PWD/demo-image-list
```

You now have a local set of (almost) all images required for the demo.
