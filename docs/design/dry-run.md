Dry Run
Tools like Terraform provide a command that creates a preview of the changes that are about to be made to your infrastructure. This functionality allows platform teams to run both manual and automated checks against those changes to ensure they have the expected impact on the platform.
System Design
Note this system design is for requesting a resource. The proposed flow would be the same for changes to the resource, deletions, or Promises.
  

1. The application engineer requests a resource by raising a PR on the Gitops store. This could be either in standard or dry run mode depending on the agreed business process.
2. An agent (e.g. Jenkins) responds to the PR and sends the dry run request to the platform
3. Kratix runs the Resource pipelines in dry run mode, only executing the functionality that is specified in the container.
4. The output from the dry run is written to a pipeline outputs store on the platform
5. The agent pulls from the pipeline outputs store and updates the pull request with the output
   1. Note this could be a summary, a diff, or the output files. Design TBC. 
6. Automated & manual checks and quality gates are run against the PR. 
7. Load and freeze windows are checked, and the PR is merged. An agent applies the request to the platform.
8. Kratix runs the Resource pipelines as normal. All stages are executed (more on the specific user interface below). Files are written to the statestore.
9. The GitOps agent on the destinations reconcile on the statestore.
   1. Note no destinations are changed until this point.
User Interface
The Promise writer should have be able to specify which parts of their Promise will run in dry run:
* A pipeline stage via a label on that container. 
* An entire pipeline via a label on the pipeline.
* From within the container 
   * This will be done via an environment variable.
Note, stages and pipelines included in the dry run mode will also be executed in regular mode.
Limitations
All dry run functionality will be subject to the following limitations and considerations:
* Timing gap between teams
   * If multiple people can request a dry run, and approvals don’t happen immediately it is possible for the state of the system to change between the dry run output being generated and the change being applied. Platform teams should consider controls on who can raise, approve and merge dry runs.
* Merge conflicts
   * If multiple people raise dry runs on one resource/promise faster than the approval process, this could easily lead to conflicts or unexpected outputs when those requests are merged and applied to the platform. Platform teams should ensure there is a clear process for applying the approved changes to be certain the expected request is applied last.
* Imperative actions
   * Imperative actions (for example, API calls) are not suitable for dry run, as it could result in a platform change. Given a user can write anything in the stage of a promise, it will be the responsibility of the Promise writer to ensure everything is tagged appropriately.
* Promise dry run outputs
   * When running a resource dry run, the output will be any files the resource generates. When running a Promise dry run, it could create a very large number of files depending on if it is a compound Promise, and how many resources it has.
Dry Run Design Doc
Problem Context
Platform teams need confidence before changes land. Tools like Terraform provide a plan command: a preview of what is about to change, so teams can run automated and manual checks before committing. Kratix has no equivalent today.
When an application engineer raises a resource request, or a Promise is updated, the changes go straight to pipelines and destinations. There is no way to see what the pipeline would produce before it runs, or to gate the change behind an approval process. For teams operating production platforms with strict change management requirements, this is a blocker.
Goals and Non-Goals
Goals
* Platform teams can request a dry run of a resource request, resource change, deletion, or Promise change before applying it.
* The dry run executes selected pipeline stages and produces output that can be inspected, diffed, and used in automated quality gates.
* Promise writers control which parts of their pipeline run in dry run mode.
* The solution integrates naturally with GitOps-based workflows and CI/CD agents.
Non-Goals
* Dry run does not replace regular pipeline execution. Stages marked for dry run will also run in normal mode.
* Destination changes are never made during a dry run. No resources are changed on destinations until a request is fully applied.
* The output format is not finalised in this design. Whether dry run produces a summary, a diff, or the full output files is TBC.
Proposed Solution
Kratix will support a dry run mode for resource and Promise pipelines. The Promise writer marks stages or entire pipelines as dry-run-eligible via a label. When a dry run is triggered (by an agent or manually), Kratix executes only the marked stages and writes their output to a pipeline outputs store. An external agent (such as Jenkins) can then pull that output, update a pull request, and trigger quality gates. Nothing reaches destinations until the request is merged and applied normally.
Design
The flow for a resource request dry run:
1. The application engineer raises a PR on the GitOps store, either in standard or dry run mode, depending on the team's change management process.
2. A CI/CD agent responds to the PR and sends the dry run request to the platform.
3. Kratix runs the resource pipelines in dry run mode, executing only the stages flagged for dry run.
4. Dry run output is written to a pipeline outputs store on the platform.
5. The agent pulls from the outputs store and updates the PR with the results (summary, diff, or files; format TBC).
6. Automated and manual checks run against the PR.
7. Load and freeze windows are checked, and the PR is merged. An agent applies the request.
8. Kratix runs the full resource pipelines as normal. All stages execute, and files are written to the state store.
9. GitOps agents on the destinations reconcile against the state store. No destination changes happen before this point.
This same flow applies to resource changes, deletions, and Promise changes.
User Interface
The Promise writer controls dry run participation at three levels of granularity:
* A pipeline stage, via a label on the container.
* An entire pipeline, via a label on the pipeline.
* From within the container, via an environment variable.
Stages and pipelines marked for dry run will also execute in regular mode.
Alternatives Considered
None documented at this stage. The system design above represents the current proposal.
Open Questions
* What is the output format? Options include a summary, a diff, or the full output files. This needs a decision before implementation.
* How does a user trigger a dry run manually (outside of a CI/CD agent flow)?
* How does this interact with compound Promises? A Promise dry run could generate a very large number of output files.
Limitations
These are known constraints that platform teams should account for when adopting dry run:
Timing gap. If multiple engineers can raise dry runs and approvals do not happen immediately, the state of the system may change between when the dry run output was generated and when the change is applied. Teams should define controls on who can raise, approve, and merge dry runs.
Merge conflicts. If multiple dry runs are raised on the same resource or Promise faster than the approval process, this can lead to conflicts or unexpected output when requests are merged. Teams should define a clear process for applying approved changes.
Imperative actions. Imperative actions such as API calls are not suitable for dry run, as they could cause platform changes. It is the Promise writer's responsibility to ensure anything with side effects is tagged appropriately.
Promise dry run output volume. A resource dry run produces only the files that resource would generate. A Promise dry run could produce a large number of files depending on whether it is a compound Promise and how many resources are associated with it.
Parties Involved
TBC
Timeline and Milestones
TBC