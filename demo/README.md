# Low internet demo run

## Reqs

If you intend to to run this in a low-internal environment we recommend pulling
and saving most (see last section for why this isn't fully offline by default) the required images before hand.
To generate a list of all the images required follow the below instructions, running
all the commands from within the `demo` directory.

### Generating the `demo-image-list`
1. run demo start to finish with full internet access
2. run the following two commands on both the platform and worker clusters
  ```
  kubectl get pods --all-namespaces -o jsonpath="{.items[*].spec.containers[*].image}" |\
    tr -s '[[:space:]]' '\n' |\
    sort |\
    uniq
  ```
  ```
  kubectl get pods --all-namespaces -o jsonpath="{.items[*].spec.initContainers[*].image}" |\
    tr -s '[[:space:]]' '\n' |\
    sort |\
    uniq
  ```
  > Note: You can run the outputs through `| sort | uniq` to result in a single ordered list
3. remove the kratix specific images as these are dynamically calculated in the `download-images` script
4. save the contents to `demo-image-list` file

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
docker exec -it <platform/worker>-control-plane crictl pull <image>
```

You now have a cluster thats ready to be run in an offline mode.
