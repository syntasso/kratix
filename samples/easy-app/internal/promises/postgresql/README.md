# PostgreSQL

This Promise provides Postgresql-as-a-Service. The Promise has 3 fields:
* `.spec.env`
* `.spec.teamID`
* `.spec.dbName`

Check the CRD documentation for more information.


To install:
```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/postgresql/promise.yaml
```

To make a resource request (small by default):
```
kubectl apply -f https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/postgresql/resource-request.yaml
```

## Development

For development see [README.md](./internal/README.md)

## Questions? Feedback?

We are always looking for ways to improve Kratix and the Marketplace. If you run into issues or have ideas for us, please let us know. Feel free to [open an issue](https://github.com/syntasso/kratix-marketplace/issues/new/choose) or [put time on our calendar](https://www.syntasso.io/contact-us). We'd love to hear from you.