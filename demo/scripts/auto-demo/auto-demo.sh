#!/bin/bash

########################
# include the magic
########################
ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/" &> /dev/null && pwd )
source "${ROOT}/demo-magic.sh"

# go to demo dir
cd "${ROOT}/../../app-as-a-service"

DEMO_PROMPT="${GREEN}➜ ${CYAN} kratix-demo ${COLOR_RESET}"

# hide the evidence
clear


function sync_worker_flux() {
  flux reconcile source bucket kratix-workload-crds --namespace flux-system --context kind-worker > /dev/null 2>&1
  flux reconcile source bucket kratix-workload-resources --namespace flux-system --context kind-worker > /dev/null 2>&1
  flux reconcile kustomization kratix-workload-crds --namespace flux-system --context kind-worker > /dev/null 2>&1
  flux reconcile kustomization kratix-workload-resources --namespace flux-system --context kind-worker > /dev/null 2>&1
}

function sync_platform_flux() {
  flux reconcile source bucket platform-cluster-worker-1-crds --namespace flux-system --context kind-platform > /dev/null 2>&1
  flux reconcile source bucket platform-cluster-worker-1-resources --namespace flux-system --context kind-platform > /dev/null 2>&1
  flux reconcile kustomization platform-cluster-worker-1-crds --namespace flux-system --context kind-platform > /dev/null 2>&1
  flux reconcile kustomization platform-cluster-worker-1-resources --namespace flux-system --context kind-platform > /dev/null 2>&1
}

########################
# Kratix Demo
########################

# using Kind we've created two K8s clusters, platform and worker
pe "kind get clusters"

# Kratix is installed on the platform cluster
# We can see Kratix is ready by asking for Promises
pe "kubectl get promises"

# There are none yet, but K8s knows what they are
# Now let's install our first Promise
# We don't need to go into detail about the Promise structure, but the file is simply a valid K8s document
pe "kubectl create -f promise.yaml"
# The below runs immediately (automatically) to win Flux timing
# to show that first just app-as-a-service exists
pei "kubectl get promises"

# sync_platform_flux

# If we ask for Promises again, we'll see the AaaS Promise
# We'll also, soon after, see that the lower-level Promises are also installed
# We'll talk more about the power of this later
# (By the time you manually run this, it should show all 5 Promises)
pe "kubectl get promises"

# The Promise has been installed, which means that the API that the Promise author wrote has also been installed
# We can see the "apps" CRD installed and ready
pe "kubectl get crds | grep app"

# Now we can switch hats to be the application developer to request an instance of AaaS
# The request, like the Promise, is just a valid K8s document
# The request for the Promise is based on the API defined in the Promise
pe "bat resource-request.yaml"

# We send the request to the platform
pe "kubectl apply -f resource-request.yaml"

# SLIDE
# for flow of resource request
# The AaaS Promise receives the single request
# That creates requests for each of the three lower-level Promises
# The requests are put in our GitOps repository
# Our GitOps repository is polled by our worker cluster
# Which then creates the workloads for each of the lower-level Promises

# Show requests on platform
# To show that the AaaS Promise generated requests for the lower-level Promises
# We can see what exists on the platform for each of those Promises
# There's a time element
pe "kubectl get apps.example.promise.syntasso.io"
pe "watch pods platform"
# sync_platform_flux

# These will take longer
# pe "kubectl get redis.marketplace.kratix.io"
# pe "kubectl get postgresqls.marketplace.kratix.io"
# pe "kubectl get slacks.marketplace.kratix.io"
# pe "kubectl get deployments.marketplace.kratix.io"
# sync_worker_flux

# SLACK
# Show channel before moving on

# (ONLY IF CRDS aren't there) Check pipelines
# pe "kubectl get pods --context=kind-platform"

# Check worker pods
# Those requests came in to the platform
# And at least some of the requests have been fulfilled on the worker cluster
# Let's see the Pods
# Check Knative
# Knative downloading many images for all the stuff it needs
pe "watch pods worker"

pe '# http://todo.local.gd:31338'

