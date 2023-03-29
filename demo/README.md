# Demo playbook

- Accompanying Google Slides are likely located [here](https://drive.google.com/drive/folders/19XyhhSky0SbjneWtNnUbwT9-_yp_td7R?usp=share_link)

## Pre-demo setup

### Set up clusters and install Kratix

- `lpass login`: Log in to the lastpass CLI for installing the Slack secret.
  - If you don't have internet, or are worried you might not you can save
  the secret (`lpass show 6120120669854427362 --password`) to a file and
  then export `LPASS_SLACK_URL` to contain the value. This will make the setup
  script skip the lpass login.
- (Optional, recommended) Download the images for offline usage. This also just speeds
up the demo massively as the images don't have to get pulled while demoing.
  1. *Generating the list of image*: Inside `demo/` there is a `demo-image-list` file containing the list of all
  images that need to be downloaded. **This can become stale as the demo gets updated**, to ensure its up-to-date
  run through the steps in [save images locally](./low-internet.md)
  1. *Downloading the images*: run `./scripts/fetch-demo-images` to fetch all the
  images.

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

## Demo

Change into the `app-as-a-service` directory for the demo.

```
cd app-as-a-service/
```

If you want to automatically have the app open when the TODO app is running, in
a separate hidden terminal run `./scripts/wait-and-open-browser-when-app-ready`.
This will wait until all the resources are created and open your browser to the todo app
when its ready.
```
../scripts/wait-and-open-browser-when-app-ready
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

### Show the app
*NOTE*: If your using the `wait-and-open-browser-when-app-ready` script then the browser
will automatically open when the apps ready.


When Postgres and TODO app are running you can connect to the app. If your NOT
using the `wait-and-open-browser-when-app-ready` then navigate manually to
http://todo.local.gd:31338/
