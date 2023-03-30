# Demo playbook

- Accompanying Google Slides are likely located [here](https://drive.google.com/drive/folders/19XyhhSky0SbjneWtNnUbwT9-_yp_td7R?usp=share_link)

### Prepare machine for demo

- Terminal
  - Make font size huge
  - Adjust PS1 to as short as possible
  - White background/theme
- Open Chrome
- Mute notifications for Slack & others (ie, enable focus mode on Mac for however long you need)
- Have Slack open on the demo channel, with no threads open.

### Prepare environment for demo

- For installing the Slack secret
  - `lpass login`
  - `export LPASS_SLACK_URL=$(lpass show 6120120669854427362 --password)`
- For getting the right version of Kratix
  - `gco main`
  - `git pull -r`
- For speeding up the demo by downloading and loading all images locally
  - `./scripts/generate-demo-image-list.sh`
  - `./scripts/fetch-demo-images`
- For installing Kratix
  - `./scripts/setup`
- For automatically opening the browser when everything is ready
  - Open new (hidden) terminal tab
  - `./scripts/wait-and-open-browser-when-app-ready`
- For starting in the right directory
  - `cd app-as-a-service/`

## Run the demo

### Install the Promise

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

### Make the resource request

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

### Show the app
*NOTE*: If your using the `wait-and-open-browser-when-app-ready` script then the browser
will automatically open when the apps ready.

When Postgres and TODO app are running you can connect to the app. If you are NOT
using the `wait-and-open-browser-when-app-ready` then navigate manually to
http://todo.local.gd:31338/
