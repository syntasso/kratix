# NGINX Ingress

This Promise provides access to the [NGINX Ingress Controller (NIC)](https://docs.nginx.com/nginx-ingress-controller/)
in the global deployment configuration. Since this configuration only deploys a
single instance per destination, there is no need to request individual resources once
the Promise is installed. For more details about the difference, see the NGINX
documents [here](https://docs.nginx.com/nginx-ingress-controller/installation/running-multiple-ingress-controllers/).

To install, run the following command while targeting your Platform cluster:
```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/nginx-ingress/promise.yaml
```

To verify the Promise is installed, you can run the following command while
targeting a worker cluster:
```
kubectl get --namespace default deployment/nginx-nginx-ingress
```

NGINX is not being provided as-a-Service with this Promise. Therefore, there's no
Resource Request: installing the Promise will install NGINX.

## Development

For development see [README.md](./internal/README.md)

## Questions? Feedback?

We are always looking for ways to improve Kratix and the Marketplace. If you run into issues or have ideas for us, please let us know. Feel free to [open an issue](https://github.com/syntasso/kratix-marketplace/issues/new/choose) or [put time on our calendar](https://www.syntasso.io/contact-us). We'd love to hear from you.
