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

To make a resource request:

```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix/main/samples/app-as-a-service-demo/resource-request.yaml
```

This resource request deploys the Kratix [sample Golang app](https://github.com/syntasso/sample-golang-app).

To test the sample app once it is successfully deployed, port forward with command below and access at `http://todo.default.local.gd:8081`:

```
kubectl --context kind-worker --namespace kourier-system port-forward svc/kourier 8081:80
```

## Development

For development see [README.md](./internal/README.md)

## Questions? Feedback?

We are always looking for ways to improve Kratix and the Marketplace. If you run into issues or have ideas for us, please let us know. Feel free to [open an issue](https://github.com/syntasso/kratix-marketplace/issues/new/choose) or [put time on our calendar](https://www.syntasso.io/contact-us). We'd love to hear from you.
