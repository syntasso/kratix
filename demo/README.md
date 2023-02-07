# Demo playbook

- Accompanying Google Slides are likely located [here](https://drive.google.com/drive/folders/19XyhhSky0SbjneWtNnUbwT9-_yp_td7R?usp=share_link)

## Pre-demo setup

### Set up clusters and install Kratix

#### Regular demo, regular Internet

- `lpass login`: Log in to the lastpass CLI for installing the Slack secret
- (Optional) [save images locally](./low-internet.md) for low internet
- Run the setup script:

```
./scripts/setup
```

### Prepare machine for demo

- Terminal
  - Make font size huge
  - Adjust PS1 to as short as possible
  - White background/theme
- Open Chrome
- Mute notifications for Slack & others (ie, enable focus mode on Mac for however long you need)
- Have Slack open on the demo channel, with no threads open.

### Set up Automagical Demo Launcher™️

If you want to automatically have the app open at the end of the demo when the TODO app is running, use the script below. It:

- Waits until all resources are created and ready
- Opens the port-forward
- Opens the browser to the todo app

In a separate window/tab, run:

```
./scripts/wait-and-open-browser-when-app-ready
```

## Demo

Change into the `app-as-a-service` directory for the demo.

```
cd app-as-a-service/
```

### Installing the Promise

Show Kratix is installed but no promises are installed:

```
kubectl get promises
```

Install AaaS Promise definition:

```
kubectl create -f promise.yaml
```

Show Promises are installed (AaaS will show first, then all):

```
kubectl get promises
```

Before switching over to being an application developer, show how Kratix set up the API for the resource request you will be making.

Show the App CRD is installed:

```
kubectl get crds | grep app
```

(Optional) Show the installed worker cluster resources:

```
kubectl --context kind-worker get pods
```

### Making the resource request

Show what a resource request looks like (using [bat for pretty output](https://github.com/sharkdp/bat)):

```
bat resource-request.yaml
```

Change the `.spec.name` of the resource request to something unique.
| :warning: WARNING |
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

Show slide in demo to show what happens on the platform when the request comes in.

Show pods for the instances that are coming up on `kind-worker`:

```
kubectl --context=kind-worker get pods
```

_NOTE_: If you are using the `wait-and-open-browser-when-app-ready` script then the browser will automatically open when the apps ready, and it will also run the port-forward for you.

### Show the app

If you are _NOT_ using the `wait-and-open-browser-when-app-ready` then once the Redis,
Postgres and TODO app (serverless so it might disappear after a while) are running start a port-forward:

```
kubectl --namespace knative-serving port-forward svc/kourier 8081:80
```

Show the app working by going to http://todo.default.local.gd:8081
