# Kratix

![Kratix](docs/deprecated/images/white_logo_color_background.jpg)

κρατήστε μια υπόσχεση | *kratíste mia ypóschesi* | **Keep a promise**

[![syntasso](https://circleci.com/gh/syntasso/kratix.svg?style=shield)](https://app.circleci.com/pipelines/github/syntasso/kratix?branch=main)

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

### Want to see Kratix in action?

* [Watch a demo of Kratix here](https://youtu.be/ZZUD2NUCBJI)

### Getting Started

Check our documentation on [kratix.io](https://kratix.io)
### Contents
- Context
  - [The Value of Kratix](https://syntasso.github.io/kratix-docs/docs/main/value-of-kratix?utm_source=github&utm_medium=readme&utm_campaign=kratix)
  - [Crossing the Platform Gap](https://www.syntasso.io/post/crossing-the-platform-gap)
  <!-- - [Personas](./docs/personas.md)  -->
  <!-- - [Team Story](./docs/success.md) -->
  <!-- - [Architecture](./docs/writing-a-promise.md) -->
- Get started with Kratix
  - [Quick Start Tutorial](https://syntasso.github.io/kratix-docs/docs/workshop/intro?utm_source=github&utm_medium=readme&utm_campaign=kratix)
  - [Promise Samples](./samples)
- [How Kratix Compares to Other Technologies in the Ecosystem](https://syntasso.github.io/kratix-docs/docs/main/value-of-kratix#comparison-with-other-tools?utm_source=github&utm_medium=readme&utm_campaign=kratix)
- [FAQs](https://syntasso.github.io/kratix-docs/docs/main/faq?utm_source=github&utm_medium=readme&utm_campaign=kratix)
- [Known issues](./docs/deprecated/known-issues.md)

**[Work with Kratix's originators, Syntasso, to deliver your organisation's Platform-as-a-Product.](https://www.syntasso.io/platform-journeys)**

### **Give feedback on Kratix**
  - [Via email](mailto:feedback@syntasso.io?subject=Kratix%20Feedback)
  - [Google Form](https://forms.gle/WVXwVRJsqVFkHfJ79)
