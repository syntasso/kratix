# Dry-Run Prototype Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add prototype dry-run support so a ResourceRequest labelled `kratix.io/dry-run: "true"` runs pipeline containers with `KRATIX_DRY_RUN=true`, writes output to a dedicated dry-run Destination, and signals completion via the `WorksSucceeded` condition with reason `DryRunSucceeded` — without touching live destinations.

**Architecture:** End-to-end label propagation: the dry-run label on the ResourceRequest flows through the pipeline Job (env var injection), to the Work object (label stamp), to the Scheduler (destination override), and finally to the WorksSucceeded condition reason. Status updates are no-oped in dry-run mode. Stale dry-run Works are cleaned up when the label is removed.

**Tech Stack:** Go, controller-runtime, Kubernetes Jobs, Kratix Work/WorkPlacement/Destination CRDs.

---

## File Map

| File | Change |
|------|--------|
| `api/v1alpha1/work_types.go` | Add `DryRunLabel` constant |
| `api/v1alpha1/pipeline_types.go` | Add `KratixDryRunEnvVar` constant |
| `api/v1alpha1/pipeline_factory.go` | `IsDryRun()` method; inject env var in `defaultEnvVars()` and `workCreatorContainer()` |
| `work-creator/lib/work_creator.go` | Stamp Work with dry-run label; include label in lookup |
| `work-creator/cmd/update_status.go` | No-op when `KRATIX_DRY_RUN=true` |
| `internal/controller/scheduler.go` | Override selectors for dry-run Works; exclude dry-run Destinations from normal scheduling |
| `lib/resourceutil/util.go` | Add `DryRunWorksSucceededReason` constant and `MarkResourceRequestAsDryRunWorksSucceeded` |
| `internal/controller/dynamic_resource_request_controller.go` | Use dry-run reason in `updateWorksSucceededCondition`; clean up stale dry-run Works |

---

## End-to-End Flow (for reference)

```
RR (kratix.io/dry-run: "true")
  ↓ DynamicResourceRequestController
  → pipeline_factory injects KRATIX_DRY_RUN=true into all containers
  ↓ Pipeline Job runs
  → user containers see KRATIX_DRY_RUN=true, can skip side effects
  → work-creator creates Work(kratix.io/dry-run: "true")
  → status-writer no-ops
  ↓ Scheduler sees Work label
  → overrides selectors to {kratix.io/dry-run: "true"}
  → creates WorkPlacement to dry-run Destination
  ↓ WorkPlacementController writes to dry-run Destination (unchanged code path)
  ↓ DynamicResourceRequestController
  → sets WorksSucceeded condition, Reason=DryRunSucceeded
  
On label removal (GitOps agent applies RR without label):
  ↓ DynamicResourceRequestController
  → deletes stale dry-run Works → triggers WorkPlacement cleanup
  → normal configure workflow runs on next reconcile
```

---

## Task 1: Add dry-run constants

**Files:**
- Modify: `api/v1alpha1/work_types.go` (after line 48, `WorkTypeStaticDependency`)
- Modify: `api/v1alpha1/pipeline_types.go` (after line 51, `KratixClusterScopedEnvVar`)

- [ ] **Step 1: Add DryRunLabel to work_types.go**

  In `api/v1alpha1/work_types.go`, extend the `const` block:
  ```go
  WorkTypeStaticDependency = "static-dependency"
  DryRunLabel              = KratixPrefix + "dry-run"
  ```

- [ ] **Step 2: Add KratixDryRunEnvVar to pipeline_types.go**

  In `api/v1alpha1/pipeline_types.go`, extend the env-var constants block:
  ```go
  KratixClusterScopedEnvVar   = "KRATIX_CLUSTER_SCOPED"
  KratixDryRunEnvVar          = "KRATIX_DRY_RUN"
  ```

- [ ] **Step 3: Verify it compiles**

  ```bash
  go build ./api/...
  ```
  Expected: no output (clean build).

- [ ] **Step 4: Commit**

  ```bash
  git add api/v1alpha1/work_types.go api/v1alpha1/pipeline_types.go
  git commit -m "feat(dry-run): add DryRunLabel and KratixDryRunEnvVar constants"
  ```

---

## Task 2: Inject KRATIX_DRY_RUN=true into pipeline containers

**Files:**
- Modify: `api/v1alpha1/pipeline_factory.go`

The pipeline Job has three Kratix-managed containers that need the env var:
- **user containers** — served by `defaultEnvVars()` which is already merged into their `Env`
- **status-writer** — uses `append(env, p.defaultEnvVars()...)`, so it inherits from `defaultEnvVars()` automatically
- **work-creator** — has its own hardcoded env slice; must be updated explicitly

- [ ] **Step 1: Add IsDryRun() method to PipelineFactory**

  Add after the `Resources()` method (after line 80 in `pipeline_factory.go`):
  ```go
  func (p *PipelineFactory) IsDryRun() bool {
  	return p.ResourceWorkflow &&
  		p.ResourceRequest != nil &&
  		p.ResourceRequest.GetLabels()[DryRunLabel] == "true"
  }
  ```

- [ ] **Step 2: Append env var in defaultEnvVars()**

  In `defaultEnvVars()` (line 160), append the env var before returning:
  ```go
  func (p *PipelineFactory) defaultEnvVars() []corev1.EnvVar {
  	// ... existing var declarations and assignments (keep untouched) ...
  	envVars := []corev1.EnvVar{
  		{Name: KratixActionEnvVar, Value: string(p.WorkflowAction)},
  		{Name: KratixTypeEnvVar, Value: string(p.WorkflowType)},
  		{Name: KratixPromiseNameEnvVar, Value: p.Promise.GetName()},
  		{Name: KratixPipelineNameEnvVar, Value: p.Pipeline.Name},
  		{Name: KratixObjectKindEnvVar, Value: objKind},
  		{Name: KratixObjectGroupEnvVar, Value: objGroup},
  		{Name: KratixObjectVersionEnvVar, Value: objVersion},
  		{Name: KratixObjectNameEnvVar, Value: objName},
  		{Name: KratixObjectNamespaceEnvVar, Value: objNamespace},
  		{Name: KratixCrdPlural, Value: p.CRDPlural},
  		{Name: KratixClusterScoped, Value: strconv.FormatBool(p.ClusterScoped)},
  	}
  	if p.IsDryRun() {
  		envVars = append(envVars, corev1.EnvVar{Name: KratixDryRunEnvVar, Value: "true"})
  	}
  	return envVars
  }
  ```
  Note: the existing function body assigns the env vars inline in the return statement. Refactor to use a named `envVars` slice so the append can happen before the return. Keep all existing variable declarations for `objNamespace`, `objGroup`, etc. untouched above the slice.

- [ ] **Step 3: Add env var to workCreatorContainer()**

  `workCreatorContainer()` builds its own `Env` slice from telemetry vars only. Extend it:
  ```go
  func (p *PipelineFactory) workCreatorContainer() corev1.Container {
  	args := []string{
  		// ... unchanged ...
  	}
  	// ... unchanged resource-namespace block ...

  	env := []corev1.EnvVar{
  		{
  			Name: telemetry.TraceParentEnvVar,
  			ValueFrom: &corev1.EnvVarSource{
  				FieldRef: &corev1.ObjectFieldSelector{
  					FieldPath: fmt.Sprintf("metadata.annotations['%s']", telemetry.TraceParentAnnotation),
  				},
  			},
  		},
  		{
  			Name: telemetry.TraceStateEnvVar,
  			ValueFrom: &corev1.EnvVarSource{
  				FieldRef: &corev1.ObjectFieldSelector{
  					FieldPath: fmt.Sprintf("metadata.annotations['%s']", telemetry.TraceStateAnnotation),
  				},
  			},
  		},
  	}
  	if p.IsDryRun() {
  		env = append(env, corev1.EnvVar{Name: KratixDryRunEnvVar, Value: "true"})
  	}

  	return corev1.Container{
  		Name:    "work-writer",
  		Image:   os.Getenv("PIPELINE_ADAPTER_IMG"),
  		Command: []string{"/bin/pipeline-adapter"},
  		Args:    args,
  		Env:     env,
  		VolumeMounts: []corev1.VolumeMount{
  			{MountPath: "/work-creator-files/input", Name: "shared-output"},
  			{MountPath: "/work-creator-files/metadata", Name: "shared-metadata"},
  			{MountPath: "/work-creator-files/kratix-system", Name: "promise-scheduling"},
  		},
  		SecurityContext: kratixSecurityContext,
  		ImagePullPolicy: DefaultImagePullPolicy,
  		Resources:       *DefaultResourceRequirements,
  	}
  }
  ```

- [ ] **Step 4: Verify it compiles**

  ```bash
  go build ./api/...
  ```
  Expected: no output.

- [ ] **Step 5: Commit**

  ```bash
  git add api/v1alpha1/pipeline_factory.go
  git commit -m "feat(dry-run): inject KRATIX_DRY_RUN=true into pipeline containers when RR is dry-run"
  ```

---

## Task 3: Stamp Work with dry-run label in work-creator

**Files:**
- Modify: `work-creator/lib/work_creator.go`

The Work is created/updated at lines 208–257. Labels are set at lines 217–231. We add the dry-run label to both the Work and the lookup labels so that `GetWork` finds the dry-run Work (not a non-dry-run one) on subsequent reconciles.

- [ ] **Step 1: Add dry-run label to Work and workLabels in Execute()**

  After line 224 (`workLabels := resourceutil.GetWorkLabels(...)`), add:
  ```go
  workLabels := resourceutil.GetWorkLabels(promiseName, resourceName, resourceNamespace, pipelineName, workflowType)
  if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
  	workLabels[v1alpha1.DryRunLabel] = "true"
  }
  ```
  The `workLabels` map is then merged into `work.Labels` at line 226–231, so the Work object automatically gets `kratix.io/dry-run: "true"`. The `GetWork(w.K8sClient, namespace, work.GetLabels())` call at line 233 then uses the label-inclusive selector, ensuring it only finds a previously-created dry-run Work (not a normal Work with the same base labels).

- [ ] **Step 2: Verify it compiles**

  ```bash
  go build ./work-creator/...
  ```
  Expected: no output.

- [ ] **Step 3: Commit**

  ```bash
  git add work-creator/lib/work_creator.go
  git commit -m "feat(dry-run): stamp Work with kratix.io/dry-run label when running in dry-run mode"
  ```

---

## Task 4: No-op status updates in dry-run mode

**Files:**
- Modify: `work-creator/cmd/update_status.go`

The status-writer runs as the main container of the pipeline Job. In dry-run mode it should exit cleanly without writing anything to the ResourceRequest status.

- [ ] **Step 1: Early return in runUpdateStatus when KRATIX_DRY_RUN is set**

  In `runUpdateStatus()` (line 34), add at the top of the function body:
  ```go
  func runUpdateStatus(ctx context.Context) error {
  	if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
  		fmt.Println("dry-run mode: skipping status update")
  		return nil
  	}
  	// ... existing code unchanged ...
  }
  ```

- [ ] **Step 2: Verify it compiles**

  ```bash
  go build ./work-creator/...
  ```
  Expected: no output.

- [ ] **Step 3: Commit**

  ```bash
  git add work-creator/cmd/update_status.go
  git commit -m "feat(dry-run): skip status updates when running in dry-run mode"
  ```

---

## Task 5: Route dry-run Works to the dry-run Destination

**Files:**
- Modify: `internal/controller/scheduler.go`

Two changes are needed:
1. In `reconcileWorkloadGroup`, override the destination selectors to `{kratix.io/dry-run: "true"}` when the Work carries the dry-run label.
2. In `getDestinationsForWorkloadGroup`, exclude dry-run Destinations from non-dry-run scheduling (so normal Works never accidentally land there).
3. In `updateWorkPlacement` (called when a WorkPlacement already exists for a resource request), also apply the dry-run selector override so an existing dry-run WorkPlacement is not incorrectly marked as misplaced.

- [ ] **Step 1: Override selectors in reconcileWorkloadGroup**

  In `reconcileWorkloadGroup` (line 190), just before line 231 (`destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)`), restructure to:
  ```go
  // (existing code: get existingWorkplacements, handle resource-request update path)
  // ...

  destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)
  if work.GetLabels()[v1alpha1.DryRunLabel] == "true" {
  	destinationSelectors = map[string]string{v1alpha1.DryRunLabel: "true"}
  }
  targetDestinationNames, err := s.getTargetDestinationNames(ctx, destinationSelectors, work)
  ```

- [ ] **Step 2: Pass dry-run state into updateWorkPlacement**

  Change the signature of `updateWorkPlacement` to accept a `dryRun bool` parameter, and use it to override selectors:

  ```go
  func (s *Scheduler) updateWorkPlacement(ctx context.Context, workloadGroup v1alpha1.WorkloadGroup, workPlacement *v1alpha1.WorkPlacement, workGeneration int64, dryRun bool) (bool, error) {
  	misplaced := true
  	destinationSelectors := resolveDestinationSelectorsForWorkloadGroup(workloadGroup)
  	if dryRun {
  		destinationSelectors = map[string]string{v1alpha1.DryRunLabel: "true"}
  	}
  	destinations, err := s.getDestinationsForWorkloadGroup(ctx, destinationSelectors)
  	// ... rest unchanged ...
  ```

  Update the single call site inside `reconcileWorkloadGroup` (around line 204):
  ```go
  isDryRun := work.GetLabels()[v1alpha1.DryRunLabel] == "true"
  // ...
  misplaced, err := s.updateWorkPlacement(ctx, workloadGroup, &existingWorkplacement, work.GetGeneration(), isDryRun)
  ```

- [ ] **Step 3: Exclude dry-run Destinations from normal scheduling**

  In `getDestinationsForWorkloadGroup` (line 519), extend the filter loop:
  ```go
  isDryRunScheduling := destinationSelectors[v1alpha1.DryRunLabel] == "true"
  destinations := []v1alpha1.Destination{}
  for _, destination := range destinationList.Items {
  	if !destination.DeletionTimestamp.IsZero() ||
  		(len(destinationSelectors) == 0 && (destination.Spec.StrictMatchLabels && len(destination.GetLabels()) > 0)) {
  		continue
  	}
  	// Never route normal Works to the dry-run Destination
  	if !isDryRunScheduling && destination.GetLabels()[v1alpha1.DryRunLabel] == "true" {
  		continue
  	}
  	destinations = append(destinations, destination)
  }
  return destinations, nil
  ```

- [ ] **Step 4: Verify it compiles**

  ```bash
  go build ./internal/controller/...
  ```
  Expected: no output.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/controller/scheduler.go
  git commit -m "feat(dry-run): route dry-run Works to dry-run Destination; exclude it from normal scheduling"
  ```

---

## Task 6: Add DryRunSucceeded condition reason

**Files:**
- Modify: `lib/resourceutil/util.go`

- [ ] **Step 1: Add constant and helper function**

  In the `const` block (after line 37, `workflowSuspendedReason`), add:
  ```go
  DryRunWorksSucceededReason = "DryRunSucceeded"
  ```

  After `MarkResourceRequestAsWorksSucceeded` (after line 108), add:
  ```go
  func MarkResourceRequestAsDryRunWorksSucceeded(obj *unstructured.Unstructured) {
  	SetCondition(obj, &clusterv1.Condition{
  		Type:               WorksSucceededCondition,
  		Status:             v1.ConditionTrue,
  		Message:            "Dry-run completed: outputs written to dry-run destination",
  		Reason:             DryRunWorksSucceededReason,
  		LastTransitionTime: metav1.NewTime(time.Now()),
  	})
  }
  ```

- [ ] **Step 2: Verify it compiles**

  ```bash
  go build ./lib/resourceutil/...
  ```
  Expected: no output.

- [ ] **Step 3: Commit**

  ```bash
  git add lib/resourceutil/util.go
  git commit -m "feat(dry-run): add DryRunSucceeded reason and condition helper"
  ```

---

## Task 7: Use dry-run reason and clean up stale dry-run Works

**Files:**
- Modify: `internal/controller/dynamic_resource_request_controller.go`

Two changes:
1. In `updateWorksSucceededCondition`, use `MarkResourceRequestAsDryRunWorksSucceeded` when the RR carries the dry-run label.
2. Add `cleanupStaleDryRunWorks` — called early in `Reconcile` — to delete dry-run Works when the label has been removed from the RR, letting normal reconciliation take over on the next cycle.

- [ ] **Step 1: Use dry-run reason in updateWorksSucceededCondition**

  In `updateWorksSucceededCondition` (line 753), replace the success branch (lines 780–786):
  ```go
  if cond == nil || cond.Status != v1.ConditionTrue {
  	if rr.GetLabels()[v1alpha1.DryRunLabel] == "true" {
  		resourceutil.MarkResourceRequestAsDryRunWorksSucceeded(rr)
  	} else {
  		resourceutil.MarkResourceRequestAsWorksSucceeded(rr)
  	}
  	r.EventRecorder.Event(rr, v1.EventTypeNormal, "WorksSucceeded",
  		"All works associated with this resource are ready")
  	return true
  }
  return false
  ```

- [ ] **Step 2: Add cleanupStaleDryRunWorks method**

  Add the following method anywhere in the file (e.g. after `updateWorksSucceededCondition`):
  ```go
  // cleanupStaleDryRunWorks deletes any Works labelled as dry-run when the RR
  // no longer carries the dry-run label. Returns true if any Works were deleted.
  func (r *DynamicResourceRequestController) cleanupStaleDryRunWorks(
  	ctx context.Context,
  	logger logr.Logger,
  	rr *unstructured.Unstructured,
  	promise *v1alpha1.Promise,
  ) (bool, error) {
  	if rr.GetLabels()[v1alpha1.DryRunLabel] == "true" {
  		return false, nil
  	}

  	namespace := rr.GetNamespace()
  	if namespace == "" {
  		namespace = v1alpha1.SystemNamespace
  	}

  	workList := &v1alpha1.WorkList{}
  	selector := labels.Set{
  		v1alpha1.PromiseNameLabel:  promise.GetName(),
  		v1alpha1.ResourceNameLabel: rr.GetName(),
  		v1alpha1.DryRunLabel:       "true",
  	}.AsSelector()
  	if err := r.Client.List(ctx, workList, &client.ListOptions{
  		LabelSelector: selector,
  		Namespace:     namespace,
  	}); err != nil {
  		return false, err
  	}

  	if len(workList.Items) == 0 {
  		return false, nil
  	}

  	logging.Info(logger, "removing stale dry-run works", "count", len(workList.Items))
  	for i := range workList.Items {
  		if err := r.Client.Delete(ctx, &workList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
  			return false, err
  		}
  	}
  	return true, nil
  }
  ```

- [ ] **Step 3: Call cleanupStaleDryRunWorks in Reconcile**

  In the `Reconcile` function, after the deletion-timestamp check (after line 169):
  ```go
  if !rr.GetDeletionTimestamp().IsZero() {
  	logging.Info(logger, "deleting resource request")
  	return r.deleteResources(opts, promise, rr)
  }

  // Clean up any stale dry-run Works when the dry-run label has been removed.
  if cleaned, err := r.cleanupStaleDryRunWorks(ctx, logger, rr, promise); err != nil {
  	return ctrl.Result{}, err
  } else if cleaned {
  	// Work deletion events will re-trigger reconciliation; nothing more to do here.
  	return ctrl.Result{}, nil
  }
  ```

- [ ] **Step 4: Verify it compiles**

  ```bash
  go build ./internal/controller/...
  ```
  Expected: no output.

- [ ] **Step 5: Full build check**

  ```bash
  go build ./...
  ```
  Expected: no output.

- [ ] **Step 6: Commit**

  ```bash
  git add internal/controller/dynamic_resource_request_controller.go
  git commit -m "feat(dry-run): use DryRunSucceeded reason; clean up stale dry-run Works on label removal"
  ```

---

## Manual Smoke Test

To validate the prototype end to end after all tasks complete:

1. **Label a dry-run Destination:**
   ```bash
   kubectl label destination <your-destination> kratix.io/dry-run=true
   ```

2. **Submit a dry-run ResourceRequest:**
   ```bash
   kubectl apply -f - <<EOF
   apiVersion: <promise-group>/<version>
   kind: <ResourceKind>
   metadata:
     name: my-dry-run-request
     namespace: default
     labels:
       kratix.io/dry-run: "true"
   spec:
     # ... same as a normal request ...
   EOF
   ```

3. **Watch the pipeline Job** — verify env var is present:
   ```bash
   kubectl get job -n kratix-platform-system -l kratix.io/resource-name=my-dry-run-request
   kubectl describe pod <job-pod> | grep KRATIX_DRY_RUN
   ```
   Expected: `KRATIX_DRY_RUN=true` appears in every container's env.

4. **Check the Work label:**
   ```bash
   kubectl get work -n kratix-platform-system -l kratix.io/dry-run=true
   ```
   Expected: one Work with the dry-run label.

5. **Check WorkPlacement targets the dry-run Destination:**
   ```bash
   kubectl get workplacement -n kratix-platform-system -l kratix.io/dry-run=true -o jsonpath='{.items[*].spec.targetDestinationName}'
   ```
   Expected: the name of your dry-run Destination.

6. **Check the condition on the ResourceRequest:**
   ```bash
   kubectl get <ResourceKind> my-dry-run-request -o jsonpath='{.status.conditions[?(@.type=="WorksSucceeded")]}'
   ```
   Expected: `"reason":"DryRunSucceeded"`, `"status":"True"`.

7. **Simulate label removal (transition to live):**
   ```bash
   kubectl label <ResourceKind> my-dry-run-request kratix.io/dry-run-
   ```
   Then watch:
   ```bash
   kubectl get work -n kratix-platform-system -l kratix.io/resource-name=my-dry-run-request
   ```
   Expected: dry-run Work disappears, replaced by a new non-dry-run Work after the next reconcile.
