# Demo playbook

## Pre-demo setup

### Setup clusters
Make sure your logged into lpass before running the setup script, or export `LPASS_SLACK_URL`
with the contents of `lpass show 6120120669854427362 --password`. Optionally (recommended), if you want
to run in [low-internet mode ensure you've saved the images locally](#saving-the-images-for-low-internet)
to `demo/cached-images/`. If any images are present they will be loaded during setup:

```
./scripts/setup
```

### Prepare machine for screensharing
* Ensure terminal font size is large
* Mute notifications for slack & others
* Have slack open on the demo channel, with no threads open.

### Terminal setup
Change into the `app-as-a-service` directory for the demo.
```
cd app-as-a-service/
```

## Demo

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

Show what a resource request looks like (using [bat for pretty output](https://github.com/sharkdp/bat)):
```
bat resource-request.yaml
```

Change the `.spec.name` of the resource request to something unique.
| :warning: WARNING          |
|:---------------------------|
| Postgres is [strict on whats acceptable for DB Names](https://www.postgresql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-IDENTIFIERS), which is pulled from `.spec.name`. Stick to simple names with no special characters, e.g. `jakesapp` |

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

*NOTE*: If your using the `wait-and-open-browser-when-app-ready` script then the browser
will automatically open when the apps ready, and it will also run the port-forwad for you.


If your NOT using the `wait-and-open-browser-when-app-ready` then once the Redis,
Postgres and TODO app are running start a portforward:
```
kubectl --context kind-worker port-forward svc/nginx-nginx-ingress 8080:80
```

Show the app working by going to http://localhost:8080 and having the host header 'todo.example.com' set.

| :warning: WARNING          |
|:---------------------------|
| Switch back to platform cluster and delete the previous resource request first before proceeding |

### Making a more complicated resource request (PCI compliant) (:warning: WARNING this flow does not work, skip for demo)

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
| due to a bug with the knative operator you cannot port-forward and show the app on the 2nd run through. something about deleting the first resource request prevents following resource requests from working |


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

  cat /tmp/demo-image-list | sort | uniq > demo-image-list
  ```

### Saving the images
Now you have a `demo-image-list` file you can run the following:

```
mkdir -p cached-images
../scripts/download-images $PWD/cached-images/ $PWD/demo-image-list
```

This will save a tar of all the images.
