#!/usr/bin/env bash
# Exposes the ArgoCD server on a fixed NodePort so the UI is reachable at
# https://localhost:31380 (the node port is mapped to the host in
# hack/dev/cluster.yaml), and sets fixed dev credentials argoadmin / argoadmin.
# Idempotent: re-running patches to the same spec is a no-op.
set -euo pipefail
CTX=kind-platform

# --- expose the UI on a host-mapped NodePort ---
kubectl --context "$CTX" -n argocd patch svc argocd-server --type merge -p '{
  "spec": {
    "type": "NodePort",
    "ports": [
      {"name": "http",  "port": 80,  "protocol": "TCP", "targetPort": 8080},
      {"name": "https", "port": 443, "protocol": "TCP", "targetPort": 8080, "nodePort": 31380}
    ]
  }
}'

# --- hardcode dev credentials: argoadmin / argoadmin ---
# bcrypt("argoadmin"); $2a$ prefix (Go's bcrypt rejects htpasswd's $2y$).
BCRYPT='$2a$10$5mwFYZpZa3/5HxBC6m3M4ubKBIFLfO79TEkfUi.fqBYfvzzGzrKb6'
MTIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# declare a local account "argoadmin" that can log in, and grant it admin
kubectl --context "$CTX" -n argocd patch configmap argocd-cm --type merge \
  -p '{"data":{"accounts.argoadmin":"apiKey,login"}}'
kubectl --context "$CTX" -n argocd patch configmap argocd-rbac-cm --type merge \
  -p '{"data":{"policy.csv":"g, argoadmin, role:admin"}}'

# set the password for both argoadmin and the built-in admin to "argoadmin"
kubectl --context "$CTX" -n argocd patch secret argocd-secret --type merge -p "{
  \"stringData\": {
    \"accounts.argoadmin.password\": \"${BCRYPT}\",
    \"accounts.argoadmin.passwordMtime\": \"${MTIME}\",
    \"admin.password\": \"${BCRYPT}\",
    \"admin.passwordMtime\": \"${MTIME}\"
  }
}"

# reload accounts/password
kubectl --context "$CTX" -n argocd rollout restart deploy argocd-server
kubectl --context "$CTX" -n argocd rollout status deploy argocd-server --timeout=120s
