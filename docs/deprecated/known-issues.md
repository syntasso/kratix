## Known Issues

- Very large (in terms of bytes of yaml) Promises may fall foul of Kubernetes's annotations size limit when using `kubectl apply`. A workaround is to use `kubectl create`. We will address this in the future with dependency management between Promises.
- The demonstration installation relies upon the platform cluster running on `172.18.0.2` and making Minio available on port `31337` to the worker cluster. This works on default settings but may conflict with custom installations.
- The demonstration installation exercises the "happy path" only. Any functionality beyond setup, applying a Promise, and creating an instance, is untested.