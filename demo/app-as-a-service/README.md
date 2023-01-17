# Demo: App as a Service

This Promise provides an App-as-a-Service. This is a compound Promise that installs the following Promises:

- [Knative](https://github.com/syntasso/kratix-marketplace/tree/main/knative)
- [Postgres](https://github.com/syntasso/kratix-marketplace/tree/main/postgresql)
- [Slack](https://github.com/syntasso/kratix-marketplace/tree/main/slack)
- [Knative Service](../knative-service/) (which deploys the sample application to the Worker Cluster)

The following fields are configurable:

- name: application name
- image: application image
- dbDriver: db type, only postgresql or none are valid options currently
- region: multi-cluster region, not currently supported

To install:

```
kubectl create -f https://raw.githubusercontent.com/syntasso/kratix/main/samples/app-as-a-service-demo/promise.yaml
```

Make sure you install the Slack hook on the platform before making the resource request.

> (via the [Slack Promise](https://github.com/syntasso/kratix-marketplace/tree/main/slack) in the Marketplace) This promise uses a Slack hook to send messages to a channel. To provide access to this hook, create a secret in default namespace called slack-channel-hook with a .data.url field containing the hook. You can create it using the following command on the platform cluster (ensure you have SLACK_HOOK_URL env var exported):

```
kubectl --namespace default create secret generic \
  slack-channel-hook --from-literal=url=${SLACK_HOOK_URL}
```

To make a resource request:

```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/main/samples/app-as-a-service-demo/resource-request.yaml
```

This resource request deploys the Kratix [sample Golang app](https://github.com/syntasso/sample-golang-app).

To test the sample app once it is successfully deployed, port forward with command below and access at `http://todo.default.local.gd:8081`:

```
kubectl --context kind-worker --namespace knative-serving port-forward svc/kourier 8081:80
```

## Development

For development see [README.md](./internal/README.md)

## Questions? Feedback?

We are always looking for ways to improve Kratix and the Marketplace. If you run into issues or have ideas for us, please let us know. Feel free to [open an issue](https://github.com/syntasso/kratix-marketplace/issues/new/choose) or [put time on our calendar](https://www.syntasso.io/contact-us). We'd love to hear from you.
