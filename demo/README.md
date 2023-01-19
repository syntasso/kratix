## Generating the `demo-image-list`

1. run demo start to finish with full internet access
2. run the following two commands on both the platform and worker clusters
  ```
  kubectl get pods --all-namespaces -o jsonpath="{.items[*].spec.containers[*].image}" |\
    tr -s '[[:space:]]' '\n' |\
    sort |\
    uniq
  ```
  ```
  kubectl get pods --all-namespaces -o jsonpath="{.items[*].spec.initContainers[*].image}" |\
    tr -s '[[:space:]]' '\n' |\
    sort |\
    uniq
  ```
  > Note: You can run the outputs through `| sort | uniq` to result in a single ordered list
3. remove the kratix specific images as these are dynamically calculated in the `download-images` script
