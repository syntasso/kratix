# Reconcile Promise Workflows After Failure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the periodic reconciliation re-run a Promise's own configure workflows whether the previous run succeeded or failed, gated by the existing `workflows.reconcileAfterFailure` Kratix config field (default `true`).

**Architecture:** The `PromiseReconciler` already receives `ReconcileAfterFailure` from config (wired in #858) but only forwards it to dynamic resource controllers — it is never used for the Promise's own workflows. A failed Promise configure workflow settles at the `passiveRequeue` early-return in `Reconcile` and never reaches the success-only "schedule next reconciliation" guard. The fix mirrors the final #858 resource-controller fix across two links: (1) `passedReconciliationInterval` forces a re-run after a failed run when the flag is on; (2) the `passiveRequeue` branch schedules the periodic requeue after a genuine failure. Both gate on a real failure so in-progress runs are never restarted.

**Tech Stack:** Go, controller-runtime, Ginkgo/Gomega, envtest (unit), real KinD cluster (system tests).

## Global Constraints

- Reuse the existing shared config field `workflows.reconcileAfterFailure` (`*bool`, default `true`). Do **not** add a new or Promise-specific config field.
- Default behaviour must preserve continuous reconciliation, including re-running after a failure. Only an explicit `false` skips a failed run on the periodic reconcile.
- Only Promise (`WorkflowTypePromise` / `WorkflowActionConfigure`) workflows are in scope. The resource-request path was delivered in #858; do not change it.
- Suspended/paused workflow handling is out of scope — do not touch `reconcileSuspendedWorkflow` or pause logic.
- A genuine failure is `ConfigureWorkflowCompletedCondition` with `Status=False` and `Reason=resourceutil.ConfigureWorkflowCompletedFailedReason`. In-progress (`Reason=PipelinesInProgressReason`) must never be treated as a failure.
- All work happens on the current branch `feat/reconcile-promise-workflow-after-failure`. Reference: `docs/superpowers/specs/2026-08-05-reconcile-promise-workflow-after-failure-design.md`.

---

## File Structure

- `internal/controller/promise_controller.go` — the two-link controller fix plus a new `metav1` failure helper (Task 1).
- `internal/controller/promise_controller_test.go` — unit tests for the failed-workflow behaviour, both flag states (Task 1).
- `cmd/main.go` — generalise the `ReconcileAfterFailure` struct-field doc comment (Task 1).
- `config/samples/kratix-config.yaml` — generalise the `reconcileAfterFailure` doc comment (Task 1).
- `test/system/assets/reconcile-after-failure/promise-workflow.yaml` — new fixture: a Promise with a gated `promise.configure` pipeline (Task 2).
- `test/system/reconcile_after_failure_test.go` — new `Describe` block for the Promise-workflow path (Task 2).
- `test/system/workflow_control_test.go` — new `jobNamesForPromisePipeline` helper (Task 2).

---

## Task 1: Promise controller retry-after-failure (code + unit tests + docs)

Both controller links must land together: Link 1 alone sets the re-run label but nothing schedules the reconcile that would fire it after a failure; Link 2 alone schedules a wake-up that then does nothing. A reviewer would reject either half on its own, so they are one task.

**Files:**
- Modify: `internal/controller/promise_controller.go` (`passedReconciliationInterval` at `:2224`; the `forcePipelineRun` call site at `:968`; the `passiveRequeue` branch at `:265`)
- Modify: `cmd/main.go:122-123` (doc comment)
- Modify: `config/samples/kratix-config.yaml:11-14` (doc comment)
- Test: `internal/controller/promise_controller_test.go` (suite reconciler setup at `:78-85`; new `When` block)

**Interfaces:**
- Consumes: `PromiseReconciler.ReconcileAfterFailure bool` (already exists, `promise_controller.go:82`, populated in `cmd/main.go:342`); `resourceutil.GetCondition(*unstructured.Unstructured, clusterv1.ConditionType) *clusterv1.Condition`; existing `workflowCompletedWithFailure(*clusterv1.Condition) bool` (in `dynamic_resource_request_controller.go:1453`, same package); `resourceutil.ConfigureWorkflowCompletedCondition`; `resourceutil.ConfigureWorkflowCompletedFailedReason`; `r.nextReconciliation(logger) ctrl.Result`; `usPromise` (the `*unstructured.Unstructured` built at `promise_controller.go:255`).
- Produces: new signature `passedReconciliationInterval(completedCond *metav1.Condition, reconciliationInterval time.Duration, reconcileAfterFailure bool) bool`; new helper `promiseWorkflowCompletedWithFailure(completedCond *metav1.Condition) bool`.

- [ ] **Step 1: Set `ReconcileAfterFailure: true` in the unit-test suite reconciler**

In `internal/controller/promise_controller_test.go`, the reconciler is constructed at `:78-85`. Add the field so the default-on behaviour is exercised by the whole suite (matches the resource suite):

```go
reconciler = &controller.PromiseReconciler{
    Client:                 fakeK8sClient,
    ApiextensionsClient:    fakeApiExtensionsClient,
    Log:                    l,
    Manager:                m,
    ReconciliationInterval: controller.DefaultReconciliationInterval,
    ReconcileAfterFailure:  true,
    EventRecorder:          eventRecorder,
}
```

- [ ] **Step 2: Write the failing unit tests**

Add this `When` block inside the existing top-level `Describe("PromiseController", …)` in `internal/controller/promise_controller_test.go`, near the existing `When("the reconciliation interval is reached", …)` block (`:1679`). It mirrors that block's setup (drive the promise to steady state with the success mock, then override the completed condition):

```go
When("the previous promise.configure workflow failed", func() {
    BeforeEach(func() {
        promise = createPromise(promiseWithWorkflowPath)
        setReconcileConfigureWorkflowToReturnFinished()

        result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
            funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
        })
        Expect(err).NotTo(HaveOccurred())
        Expect(result).To(Equal(ctrl.Result{}))

        Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())

        uPromise, err := promise.ToUnstructured()
        Expect(err).NotTo(HaveOccurred())
        resourceutil.SetCondition(uPromise, &clusterv1.Condition{
            Type:               resourceutil.ConfigureWorkflowCompletedCondition,
            Status:             v1.ConditionFalse,
            Message:            "the pipeline failed",
            Reason:             resourceutil.ConfigureWorkflowCompletedFailedReason,
            LastTransitionTime: metav1.NewTime(time.Now().Add(-reconciler.ReconciliationInterval).Add(-time.Minute)),
        })
        Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())
    })

    When("reconcileAfterFailure is true (default)", func() {
        BeforeEach(func() {
            reconciler.ReconcileAfterFailure = true
        })

        It("re-runs the promise.configure workflow and schedules the next reconciliation", func() {
            result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: promise.GetName(), Namespace: promise.GetNamespace()}})
            Expect(err).NotTo(HaveOccurred())
            Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

            Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
            Expect(promise.Labels[resourceutil.ManualReconciliationLabel]).To(Equal("true"))
        })
    })

    When("reconcileAfterFailure is false", func() {
        BeforeEach(func() {
            reconciler.ReconcileAfterFailure = false
        })

        It("does not re-run the promise.configure workflow or schedule a periodic reconciliation", func() {
            result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
                funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
            })
            Expect(err).NotTo(HaveOccurred())
            Expect(result).To(Equal(ctrl.Result{}))

            Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
            Expect(promise.Labels[resourceutil.ManualReconciliationLabel]).NotTo(Equal("true"))
        })
    })
})
```

Why these expectations are deterministic:
- **true case:** `passedReconciliationInterval` now returns true for the stale failed condition, so `restartOnReconciliationInterval` sets `ManualReconciliationLabel` and returns `restarted=true` (short-circuiting *before* `reconcileConfigure`, so the mock is irrelevant). Back in `Reconcile`, `passiveRequeue=true` and `usPromise` still carries the pre-set failed condition, so Link 2 returns `nextReconciliation` → `RequeueAfter`.
- **false case:** `passedReconciliationInterval` returns false (failure gated off), so no label; `reconcileConfigure` (success mock) returns not-passive; the success-only guard sees a `False` condition and returns `ctrl.Result{}`.

The `resourceutil`, `clusterv1`, `v1`, `metav1`, `time`, and `types` imports are already used in this test file (see the existing interval block at `:1696`).

- [ ] **Step 3: Run the new tests to verify they fail**

```bash
go test ./internal/controller/ -run TestControllers -args -ginkgo.focus="the previous promise.configure workflow failed"
```
Expected: FAIL — `passedReconciliationInterval` does not yet accept a third argument (compile error), or once you stub it, the true-case `RequeueAfter`/label assertions fail because the two links are not implemented.

- [ ] **Step 4: Implement Link 1 — force re-run after a failed run**

In `internal/controller/promise_controller.go`, replace `passedReconciliationInterval` (`:2224`) and add the `metav1` failure helper immediately after it. A new helper is required because the existing `workflowCompletedWithFailure` takes `*clusterv1.Condition`, but this path uses `promise.GetCondition`, which returns `*metav1.Condition`:

```go
func passedReconciliationInterval(completedCond *metav1.Condition, reconciliationInterval time.Duration, reconcileAfterFailure bool) bool {
	if completedCond == nil || time.Since(completedCond.LastTransitionTime.Time) <= reconciliationInterval {
		return false
	}
	return completedCond.Status == metav1.ConditionTrue ||
		(reconcileAfterFailure && promiseWorkflowCompletedWithFailure(completedCond))
}

func promiseWorkflowCompletedWithFailure(completedCond *metav1.Condition) bool {
	return completedCond != nil &&
		completedCond.Status == metav1.ConditionFalse &&
		completedCond.Reason == resourceutil.ConfigureWorkflowCompletedFailedReason
}
```

Update the call site at `:968` to pass the flag:

```go
forcePipelineRun := passedReconciliationInterval(completedCond, r.ReconciliationInterval, r.ReconcileAfterFailure) &&
	promise.Labels[resourceutil.WorkflowRunFromStartLabel] != "true"
```

- [ ] **Step 5: Implement Link 2 — schedule the periodic requeue after a failure**

In `internal/controller/promise_controller.go`, replace the `passiveRequeue` branch in `Reconcile` (`:265-268`) with:

```go
if passiveRequeue {
	logging.Debug(logger, "reconciliation paused awaiting Promise configure workflow updates")
	// A failed Promise configure workflow settles here (ReconcileConfigure returns a
	// passive requeue) and never reaches the success guard later in Reconcile, so
	// schedule the periodic reconcile here to retry the run on the interval.
	if r.ReconcileAfterFailure && workflowCompletedWithFailure(
		resourceutil.GetCondition(usPromise, resourceutil.ConfigureWorkflowCompletedCondition)) {
		return r.nextReconciliation(logger), nil
	}
	return ctrl.Result{}, nil
}
```

This reuses the existing `clusterv1`-typed `workflowCompletedWithFailure` because `resourceutil.GetCondition(usPromise, …)` returns `*clusterv1.Condition`. `usPromise` is the unstructured object built at `:255` and passed to `reconcileConfigure`, so it carries the freshly-set failed condition.

- [ ] **Step 6: Run the new tests to verify they pass**

```bash
go test ./internal/controller/ -run TestControllers -args -ginkgo.focus="the previous promise.configure workflow failed"
```
Expected: PASS.

- [ ] **Step 7: Run the full controller suite to check for regressions**

```bash
go test ./internal/controller/...
```
Expected: PASS. (The `passedReconciliationInterval` signature change has exactly one call site — `:968` — so no other callers need updating.)

- [ ] **Step 8: Generalise the documentation comments**

In `cmd/main.go` (`:122-123`), change the struct-field comment from resource-only to shared wording:

```go
	// ReconcileAfterFailure controls whether the periodic reconciliation re-runs a Promise's
	// or resource request's configure workflows after the previous run failed. Defaults to true
	ReconcileAfterFailure *bool `json:"reconcileAfterFailure,omitempty"`
```

In `config/samples/kratix-config.yaml` (`:11-14`), change the comment to:

```yaml
      # reconcileAfterFailure controls whether the periodic reconciliation (on the
      # Default Reconciliation Interval) re-runs a Promise's or resource request's
      # configure workflows after the previous run failed. Defaults to true.
      reconcileAfterFailure: true
```

- [ ] **Step 9: Build and vet**

```bash
go build ./... && go vet ./internal/controller/... ./cmd/...
```
Expected: no output (success).

- [ ] **Step 10: Commit**

```bash
git add internal/controller/promise_controller.go internal/controller/promise_controller_test.go cmd/main.go config/samples/kratix-config.yaml
git commit -m "feat: reconcile promise workflows on the schedule even after a failed run

Wire the existing workflows.reconcileAfterFailure config field (default true)
into the Promise's own configure-workflow reconcile path. A failed Promise
workflow settles at the passiveRequeue early-return and never reached the
success-only reconcile guard; now, when the flag is on, that branch schedules
the periodic reconcile and passedReconciliationInterval forces the re-run.
Both are gated on a genuine failure so in-progress runs are never restarted.
When the flag is off, a failed run is skipped by the periodic reconcile and
only manual reconciliation re-runs it, preserving the previous behaviour.

Part of #862

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: System-test coverage for the Promise-workflow path

Verifies the real failed-workflow timing that mocked unit tests cannot (the #858 memory lesson: the default unit mock of `reconcileConfigure` returns a passive requeue without exercising the real failed path). Uses a **separate** fixture Promise (a gated `promise.configure` on the existing `reconcilable` promise would keep it `Unavailable`, breaking the resource specs' `BeforeEach` `Available` wait).

**Files:**
- Create: `test/system/assets/reconcile-after-failure/promise-workflow.yaml`
- Modify: `test/system/workflow_control_test.go` (add `jobNamesForPromisePipeline` near `jobNamesForResourcePipeline` at `:502`)
- Modify: `test/system/reconcile_after_failure_test.go` (add a new top-level `Describe`)

**Interfaces:**
- Consumes: existing helpers `jobCountForPromisePipeline(promiseName, pipelineName string) int` (`:479`); `newJobNames(previous, current []string) []string` (`:513`); `workflowJobNamespace("promise")` → `"kratix-platform-system"`; `workflowJobSelector`; `platform.Kubectl` / `platform.KubectlAllowFail` / `platform.EventuallyKubectlDelete`; `restartController()`; `kratixConfigPath`; the shared `kratix-config-retry.yaml` / `kratix-config-no-retry.yaml` fixtures.
- Produces: `jobNamesForPromisePipeline(promiseName, pipelineName string) []string`.

- [ ] **Step 1: Create the fixture Promise with a gated promise.configure pipeline**

Create `test/system/assets/reconcile-after-failure/promise-workflow.yaml`. The pipeline runs in `kratix-platform-system` (the Promise-workflow namespace), so the gate ConfigMap lives there and the RBAC grants same-namespace `get` on configmaps. A distinct CRD group/kind avoids clashing with the resource-workflow fixture's `reconcilables.test.kratix.io`:

```yaml
apiVersion: platform.kratix.io/v1alpha1
kind: Promise
metadata:
  name: reconcilable-promise-wf
spec:
  api:
    apiVersion: apiextensions.k8s.io/v1
    kind: CustomResourceDefinition
    metadata:
      name: reconcilablepromises.test.kratix.io
    spec:
      group: test.kratix.io
      names:
        kind: ReconcilablePromise
        plural: reconcilablepromises
        singular: reconcilablepromise
      scope: Namespaced
      versions:
        - name: v1alpha1
          served: true
          storage: true
          schema:
            openAPIV3Schema:
              type: object
              properties:
                spec:
                  type: object
                  properties:
                    message:
                      type: string
  workflows:
    promise:
      configure:
        - apiVersion: platform.kratix.io/v1alpha1
          kind: Pipeline
          metadata:
            name: promise-configure
          spec:
            jobOptions:
              backoffLimit: 0
            rbac:
              permissions:
                - apiGroups: [""]
                  resources: ["configmaps"]
                  verbs: ["get"]
            containers:
              - name: gate
                image: ghcr.io/syntasso/kratix-pipeline-utility:v0.0.1
                command: ["/bin/sh", "-c"]
                args:
                  - |
                    set -eu
                    if kubectl get configmap reconcile-after-failure-promise-gate -n kratix-platform-system >/dev/null 2>&1; then
                      echo "gate present; configure succeeds"
                      exit 0
                    fi
                    echo "gate absent; configure fails"
                    exit 1
```

- [ ] **Step 2: Add the `jobNamesForPromisePipeline` helper**

In `test/system/workflow_control_test.go`, add immediately after `jobNamesForResourcePipeline` (`:510`):

```go
func jobNamesForPromisePipeline(promiseName, pipelineName string) []string {
	output := platform.Kubectl(
		"get", "jobs",
		"-n", workflowJobNamespace("promise"),
		"-l", workflowJobSelector("promise", promiseName, pipelineName, "configure"),
		"-o=jsonpath={.items[*].metadata.name}",
	)
	return strings.Fields(output)
}
```

- [ ] **Step 3: Write the failing system-test specs**

Append a new top-level `Describe` to `test/system/reconcile_after_failure_test.go`. It has its own setup (no `Available` wait — a failing `promise.configure` keeps the Promise `Unavailable`), and asserts on the Promise's own `ConfigureWorkflowCompleted` condition and `promise-configure` job names:

```go
var _ = Describe("Reconcile promise workflow after failure", Serial, func() {
	const (
		assetsPath    = "assets/reconcile-after-failure"
		promiseName   = "reconcilable-promise-wf"
		gateConfigMap = "reconcile-after-failure-promise-gate"
		gateNamespace = "kratix-platform-system"
	)

	workflowStatusJSONPath := `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}'`

	configureJobCount := func() int {
		return jobCountForPromisePipeline(promiseName, "promise-configure")
	}
	configureJobNames := func() []string {
		return jobNamesForPromisePipeline(promiseName, "promise-configure")
	}

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete("promise", promiseName)
		platform.KubectlAllowFail("delete", "configmap", gateConfigMap, "-n", gateNamespace)
		platform.Kubectl("apply", "-f", kratixConfigPath)
		restartController()
	})

	When("reconcileAfterFailure is true", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-retry.yaml"))
			restartController()
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise-workflow.yaml"))
		})

		It("re-runs the failed promise workflow on the schedule and resumes to success", func() {
			var jobsAtFirstFailure []string
			By("failing the promise configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				jobsAtFirstFailure = configureJobNames()
			})

			By("re-running the workflow automatically", func() {
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsAtFirstFailure, configureJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
			})

			By("succeeding once the gate exists", func() {
				platform.Kubectl("create", "configmap", gateConfigMap, "-n", gateNamespace)
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})
		})
	})

	When("reconcileAfterFailure is false", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-no-retry.yaml"))
			restartController()
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise-workflow.yaml"))
		})

		It("does not re-run the failed promise workflow, but manual reconciliation works", func() {
			var failedJobs []string
			By("failing the promise configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				failedJobs = configureJobNames()
			})

			By("not re-running on the schedule", func() {
				Consistently(func(g Gomega) {
					g.Expect(newJobNames(failedJobs, configureJobNames())).To(BeEmpty())
				}, 30*time.Second, 3*time.Second).Should(Succeed())
			})

			By("re-running when manually labelled", func() {
				platform.Kubectl("label", "--overwrite", "promise", promiseName,
					"kratix.io/manual-reconciliation=true")
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(failedJobs, configureJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
			})
		})
	})
})
```

- [ ] **Step 4: Build the system-test package to catch compile errors early**

```bash
go vet ./test/system/...
```
Expected: no output. (Verifies the new helper and `Describe` compile; `time`, `filepath`, `kubeutils` are already imported in this file.)

- [ ] **Step 5: Run the new system specs against a real cluster**

Follow the repo's system-test entrypoint (e.g. `make system-test`, or the project's documented way to run `test/system`). Focus the run:

```bash
go test ./test/system/ -args -ginkgo.focus="Reconcile promise workflow after failure"
```
Expected: PASS. If the true-case "re-running the workflow automatically" step fails, that is the design's flagged risk — confirm `usPromise` carries the freshly-set failed condition in the `passiveRequeue` branch (Task 1, Step 5); the real controller is the definitive check per the #858 memory note.

- [ ] **Step 6: Commit**

```bash
git add test/system/assets/reconcile-after-failure/promise-workflow.yaml test/system/reconcile_after_failure_test.go test/system/workflow_control_test.go
git commit -m "test(system): cover reconcile-after-failure for promise workflows

A Promise with a gated promise.configure pipeline: with reconcileAfterFailure
on, a failed promise workflow re-runs on the schedule and resumes to success
once the gate exists; with it off, a failed workflow is not re-run on the
schedule but manual reconciliation still triggers it.

Closes #862

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage:**
- Spec "Part 1 — force re-run" → Task 1 Steps 4. ✓
- Spec "Part 2 — schedule wake-up requeue" → Task 1 Step 5. ✓
- Spec "Part 3 — documentation" → Task 1 Step 8. ✓
- Spec "Unit tests" → Task 1 Steps 1-2, 6. ✓
- Spec "System tests (separate fixture)" → Task 2. ✓
- Spec "config wiring already done in #858" → confirmed in Task 1 Interfaces (no cmd/main.go logic change; only a comment). ✓
- Spec "risk: usPromise freshness" → Task 2 Step 5 note. ✓
- Acceptance criteria (failed+default→re-run; success+default→re-run; failed+disabled→not re-run + manual works) → true/false unit specs (Task 1 Step 2) and true/false system specs incl. manual label (Task 2 Step 3); the success+default→re-run case is already covered by the existing `When("the reconciliation interval is reached", …)` block and remains green (Task 1 Step 7). ✓

**2. Placeholder scan:** No TBD/TODO/"handle edge cases"/"similar to". All code blocks are concrete. ✓

**3. Type consistency:** `passedReconciliationInterval` third param `reconcileAfterFailure bool` is defined (Step 4) and matched at the call site (Step 4) and via `reconciler.ReconcileAfterFailure` in tests. `promiseWorkflowCompletedWithFailure(*metav1.Condition)` (metav1) is used only in `passedReconciliationInterval`; the `passiveRequeue` branch uses the existing `workflowCompletedWithFailure(*clusterv1.Condition)` fed by `resourceutil.GetCondition` (which returns `*clusterv1.Condition`) — the two-helper split is intentional and type-correct. `jobNamesForPromisePipeline` (Task 2 Step 2) matches its call sites (Task 2 Step 3). ✓
