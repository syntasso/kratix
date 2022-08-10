# Kratix

![Kratix](docs/images/white_logo_color_background.jpg)

κρατήστε μια υπόσχεση | *kratíste mia ypóschesi* | **Keep a promise**

## What is Kratix?

Kratix is a framework that enables co-creation of capabilities by providing a clear contract between application and platform teams through the definition and creation of “Promises”. Using the GitOps workflow and Kubernetes-native constructs, Kratix provides a flexible solution to empower your platform team to curate an API-driven, bespoke platform that can easily be kept secure and up-to-date, as well as evolving as business needs change.

Promises:
- provide the right abstractions to make your developers as productive, efficient, and secure as possible. Any capability can be encoded and delivered via a Promise, and once “Promised” the capability is available on-demand, at scale, across the organisation.
- codify the contract between platform teams and application teams for the delivery of a specific service, e.g. a database, an identity service, a supply chain, or a complete development pipeline of patterns and tools.
- can be shared and reused between platforms, teams, business units, even other organisations.
- are easy to build, deploy, and update. Bespoke business logic can be added to each Promise’s pipeline.
- can create “Workloads”, which are deployed, via the GitOps Toolkit, across fleets of Kubernetes clusters.

A Promise is comprised of three elements:
- Custom Resource Definition: input from an app team to create instances of a capability.
- Worker Cluster Resources: dependencies necessary for any created Workloads.
- Request Pipeline: business logic required when an instance of a capability is requested.

### Contents
- Context
  - [The Value of Kratix](./docs/kratix-value.md) 
  - [Crossing the Platform Gap](https://www.syntasso.io/post/crossing-the-platform-gap) 
  <!-- - [Personas](./docs/personas.md)  -->
  <!-- - [Team Story](./docs/success.md) -->
  <!-- - [Architecture](./docs/writing-a-promise.md) -->
- Get started with Kratix
  - [Quick Start Tutorial](https://github.com/syntasso/workshop/blob/main/README.md)
  - [Promise Samples](./docs/promise-samples.md)
- [How Kratix Compares to Other Technologies in the Ecosystem](./docs/compare.md)
- [FAQs](docs/FAQ.md)
- [Known issues](./docs/known-issues.md)  

**[Work with Kratix's originators, Syntasso, to deliver your organisation's Platform-as-a-Product.](https://www.syntasso.io/how-we-help)**

### **Give feedback on Kratix**
  - [Via email](mailto:feedback@syntasso.io?subject=Kratix%20Feedback)
  - [Google Form](https://forms.gle/WVXwVRJsqVFkHfJ79)
