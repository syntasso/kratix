## What's the Problem? 
Syntasso understands being a Platform team member is challenging. These teams relentlessly feel tensions from many directions, and often face: 
* Demands from their customers, who increasingly expect software served from internal platforms be as simple, quick to consume, and performant as commodity public-cloud services.
* Steep learning curves as they take software from large vendors, figure out how to tweak the seemingly endless configuration options to introduce the "right" level of opinions: not too opinionated to reduce utility for their users, but opinionated enough to meet their own internal SLI/SLO requirements.
* Demands from their own internal security, audit, and compliance teams who expect all internal software to be secure, traceable, up-to-date, and compliant with industry regulations.  

## What's the Solution? 
This is where Promises can help. The aim of a Promise is simple: 
* To enable Platform teams to take complex software, modify the settings needed to meet their internal requirements, inject their own organisational opinions, and finally to expose a simplified API to _their_ users to enable frictionless creation and consumption of services that meet the needs of all stakeholders.  
   
The more Promises a platform can deliver, the richer that platform becomes. While commercial entities will provide high-quality Promises that meet the demands of a broad base of platform teams, it is inevitable any team, in an organisation at scale, will have to respond to requests to add further custom as-a-Service capabilities. 

### It's not just about Data Services
We are seeing an increase in demand for Platform teams to ship capabilities that provide higher value than simple, atomic, as-a-Service data services such as Redis or your favourite DbaaS. Data service capabilities are table stakes for a modern platform. Platform users are now demanding increasingly complex patterns of the technologies they need, on-demand, so they can be immediately productive. In other words: they want _their_ required technologies, and they want them wired together, so they can get on with delivering value to _their_ customers! These complex patterns are frequently referred to as [Golden paths](https://engineering.atspotify.com/2020/08/17/how-we-use-golden-paths-to-solve-fragmentation-in-our-software-ecosystem/) or [Paved Paths](https://medium.com/codex/what-is-a-paved-path-b2294463a3a9). 

As Promises are the unit that allow platforms to be built incrementally, platform teams can easily add low-level Promises (such as a Jenkins DBaaS, Redis etc.), and then simply wire them together into a single high-level Promise. This raises the "Value Line", reducing the cognitive load for platform users. 

Consider a Promise such as 'ACME Java Development Environment': setting up development environments is repetitive and requires many cookie-cutter steps. This Promise can encapsulate the required steps, and handle the toil of wiring up the Git repos, spinning up a CI/CD server, creating a PaaS to run the applications, instructing CI/CD to listen to the Git repos and push successful builds into the PaaS, and finally wiring applications to their required data services. All of this complexity can easily be encapsulated within a single Promise. 

### Promise Basics
A Promise is comprised of three elements:

1. `xaasCrd`: this is the CRD that is exposed to the users of the Promise. Imagine the order form for a product. What do you need to know from your customer? Size? Location? Name?
2. `xaasRequestPipeline`: this is the pipeline that will create the Jenkins resources required to run Jenkins on a worker cluster decorated with whatever you need to run Jenkins from your own Platform. Do you need to scan images? Do you need to send a request to an external API for approval? Do you need to inject resources for storage, mesh, networking, etc.? These activities happen in the pipeline.
3. `clusterWorkerResources`: this contains all of the Kubernetes resources required on a cluster for it to be able to run an instance Jenkins such as CRDs, Operators and Deployments. Think about the required prerequisites necessary on the worker cluster, so that the resources declared by your pipeline are able to converge.

Learn more about writing Promises [here](writing-a-promise.md)