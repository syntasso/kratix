# Promise Creation
Decide on the abstractions. Create Promise CRD, properties, and validation are the important bit -- lots of effort to get this right. I wonder if Kubebuilder could help here somehow. 

# Cluster Level Install
1. Install Certmanager
1. Install RMQ Cluster Operator 
1. Install RMQ messaging topology Operator 

# Instance Level: Production 
Create a dedicated production RMQ cluster
1. Transformation image will Create amqp CR to create a new Cluster and Queue

# Instance Level: Development
Hand out a slice of a RMQ for devlopment purposes.

1. Transformation image will Create amqp CR to create a user, and a Queue in the shared cluster. If the shared cluster doesn't exist, then create it. 

# Development of a pipeline
All the tools you know and love: Docker! 

We're choosing kustomize. Use a kustomize Docker image. 

We can get fast feedback by running a `docker run -ti --volume=${PWD}:/input --volume=${PWD}/output:/output amqp:dev`

When we're happy docker build && docker push -- make sure the transformationImage pipeline in Promise matches the tag of these builds

Apply the promise, apply the resource request. 