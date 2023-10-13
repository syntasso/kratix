This image will wait for the precence of `/tmp/continue` before existing 0. This
allows you to SSH onto the image and do whatever manual steps you need to do
before instructing the container to exit.

Add the following to your Promise pipeline:
```
- image: ghcr.io/syntasso/kratix-pipeline-debugger:v0.0.1
  name: manual-debug
```
