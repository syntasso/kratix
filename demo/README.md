| :warning: WARNING: demo is _low internet_ not _no internet_                                                                                                                                                                                                                                                                                                   |
| :------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Below details steps you can do ahead of the demo to speed things up, and that will help if you are in a setting with poor internet. **Note**, though, that to run the demo without having to do a bunch of stuff, it's best if you have _some_ internet for the Slack message to go through and the DNS to work at the end (so the app loads in the browser). |

# Demo playbook

- Accompanying Google Slides are likely located [here](https://drive.google.com/drive/folders/19XyhhSky0SbjneWtNnUbwT9-_yp_td7R?usp=share_link)

### 💻&nbsp;&nbsp;Prepare machine for demo

- Terminal
  - Make font size huge
  - Adjust PS1 to as short as possible
  - White background/theme
- Open Chrome
- Mute notifications for Slack & others (ie, enable focus mode on Mac for however long you need)
- Have Slack open on the demo channel, with no threads open.

### ⚙️&nbsp;&nbsp;Prepare environment for demo

#### Requires internet

- For installing the Slack secret
  - `lpass login`
  - `export LPASS_SLACK_URL=$(lpass show 6120120669854427362 --password)`
- For getting the right version of Kratix
  - `gco main`
  - `git pull -r`
- For speeding things up by downloading and loading all images locally
  - `./scripts/generate-demo-image-list.sh`<br>
    ⏳&nbsp;&nbsp;Takes a long time because it needs to set up and install Kratix. <br>
    👉🏾&nbsp;&nbsp;Only required when there is a change _to the demo_ (not other parts of Kratix).
  - `./scripts/fetch-demo-images`<br>
    💨&nbsp;&nbsp;If you've run it before, it should be quick.<br>
    Will only fetch and put _new_ versions in the `cached_images` dir.

#### Does not require internet

- For installing Kratix after images are saved locally
  - `./scripts/setup`
- For automatically opening the browser when everything is ready
  - Open new (hidden) terminal tab
  - 🪄🪄&nbsp;&nbsp;`./scripts/wait-and-open-browser-when-app-ready`<br>
    ⚠️&nbsp;&nbsp;Showing the app in the browser _does_ require internet for the DNS to work
- For starting in the right directory
  - `cd app-as-a-service/`

## 📽&nbsp;&nbsp;Run the demo

- ⚠️&nbsp;&nbsp;Showing the Slack notification and the app in the browser as part of this demo _does_ require internet for the DNS to work
- 🪄🪄&nbsp;&nbsp;`./scripts/auto-demo/auto-demo.sh` automates the steps below.

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

- ⚠️&nbsp;&nbsp; Postgres is [strict on whats acceptable for DB Names](https://www.postgresql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-IDENTIFIERS), which is pulled from `.spec.name`.<br>
  👉🏾&nbsp;&nbsp;Stick to simple names with no special characters, e.g. `jakesapp`

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

- ⚠️&nbsp;&nbsp;Showing the app in the browser as part of this demo (without manual extra steps) _does_ require internet for the DNS to work
- 🪄🪄&nbsp;&nbsp;Using `wait-and-open-browser-when-app-ready`? Browser will automatically open when the app is ready.

When Postgres and TODO app are running (i.e. worker pods are running) you can connect to the app. If you are NOT
using the `wait-and-open-browser-when-app-ready` then navigate manually to http://todo.local.gd:31338/

### Show the Slack notification

- ⚠️&nbsp;&nbsp;Showing the Slack notification as part of this demo _does_ require internet for the DNS to work
