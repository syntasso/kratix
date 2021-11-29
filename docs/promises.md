## What's the Problem? 
Syntasso understands being a Platform team member is challenging. These teams relentlessly feel tensions from many directions, and often face: 
* Demands from their customers, who increasingly expect software served from internal platforms be as simple, quick to consume, and performant as commodity public-cloud services.
* Huge learning curves as they take software from large vendors, figure out how to tweak the seemingly endless configuration options to introduce the "right" level of opinions: not too opinionated to reduce utility for their users, but opinionated enough to meet their own internal SLI/SLO requirements.
* Demands from their own internal security, audit, and compliance teams who expect all internal software to be secure, traceable, up-to-date, and compliant with industry regulations.

## What's the Solution? 
This is where Promises can help. The aim of a Promise is simple: 
* To enable Platform teams to take complex software, modify the settings needed to meet their internal requirements, inject their own organisational opinions, and finally to expose a simplified API to _their_ users to enable frictionless creation and consumption of services that meet the needs of all stakeholders.  
   
The more Promises a platform can deliver, the richer that platform becomes, and while commercial entities will provide high-quality Promises that meet the demands of a broad base of platform teams, it is inevitable teams in organisations at scale will have to respond to requests to add further custom as-a-Service capabilities. 

### Promise basics
Conceptually a Promise is Broken down into three parts:

1. `xaasCrd`: this is the CRD that is exposed to the users of the Promise. Imagine the order form for a product. What do you need to know from your customer? Size? Location? Name?
2. `xaasRequestPipeline`: this is the pipeline that will create the Jenkins resources requried to run Jenkins on a worker cluster decorated with whatever you need to run Jenkins from your own Platform. Do you need to scan images? Do you need to send a request to an external API for approval? Do you need to inject resources for storage, mesh, networking, etc.? These activities happen in the pipeline.
3. `clusterWorkerResources`: this contains all of the Kuberentes resources required on a cluster for it to be able to run an instance Jenkins such as CRDs, Operators and Deployments. Think about the required prerequisites necessary on the worker cluster, so that the resources declared by your pipeline are able to converge.

Learn more about writing Promises [here](writing-a-promise.md)

