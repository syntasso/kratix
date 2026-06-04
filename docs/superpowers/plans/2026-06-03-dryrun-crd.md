# DryRun CRD Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the label-based dry-run prototype with a first-class `DryRun` CRD that keeps dry-run concerns entirely separate from real ResourceRequests.

**Architecture:** A new `DryRunReconciler` watches `DryRun` objects, creates an ephemeral ResourceRequest (marked with `kratix.io/dry-run=true` and `kratix.io/dry-run-owner=<name>`) owned by the DryRun, polls until the DRRC sets WorksSucceeded=True on it, then diffs the resulting dry-run Works against the live Works of the referenced ResourceRequest and writes a summary Work to the dry-run Destination. The diff logic moves from work-creator into the DryRun controller. The DRRC is taught to skip its own summary-generation path for ephemeral RRs it does not own.

**Tech Stack:** Go, controller-runtime, `github.com/sergi/go-diff` (already in go.mod), Kubernetes unstructured API (for dynamic RR GVK from Promise.GetAPI())

---

## File Map

| Action | Path | Responsibility |
|---|---|---|
| Create | `api/v1alpha1/dryrun_types.go` | DryRun CRD types + scheme registration |
| Modify | `api/v1alpha1/work_types.go` | Add `DryRunOwnerLabel` constant |
| Modify | `api/v1alpha1/zz_generated.deepcopy.go` | DeepCopy methods for new types |
| Create | `internal/controller/dryrun_controller.go` | DryRun reconcile loop + diff logic |
| Modify | `cmd/main.go` | Register DryRunReconciler |
| Modify | `internal/controller/dynamic_resource_request_controller.go` | Skip `ensureDryRunSummary` for DryRun-owned ephemeral RRs |
| Modify | `work-creator/lib/work_creator.go` | Remove diff block + diff helper functions |
| Modify | `docs/github-actions/dry-run/action.yml` | Commit DryRun object instead of labelled RR |

---

## Background: how the existing code fits together

- `Promise.GetAPI()` (`api/v1alpha1/promise_types.go:302`) returns the `*schema.GroupVersionKind` for the promise's resource CRD. Use this to create the ephemeral RR as `unstructured.Unstructured`.
- `resourceutil.GetCondition(rrObj, resourceutil.WorksSucceededCondition)` works on `*unstructured.Unstructured` — use this to poll the ephemeral RR's status.
- `DryRunLabel = "kratix.io/dry-run"` and `DryRunSummaryLabel = "kratix.io/dry-run-summary"` are already in `api/v1alpha1/work_types.go`.
- `KratixDryRunEnvVar = "KRATIX_DRY_RUN"` is in `api/v1alpha1/pipeline_types.go:52`. It is injected into pipeline containers by `pipeline_factory.go` when the RR carries `DryRunLabel`. This stays unchanged.
- `ensureDryRunSummary` lives in `dynamic_resource_request_controller.go:864`. It must be skipped for DryRun-owned ephemeral RRs, but kept for any user who still applies the label directly.
- `sergi/go-diff v1.4.0` is already in `go.mod:18`. No dependency change needed.

---

## Task 1: Add `DryRunOwnerLabel` constant

**Files:**
- Modify: `api/v1alpha1/work_types.go:49-51`

- [ ] **Step 1: Add the constant**

In `api/v1alpha1/work_types.go`, add `DryRunOwnerLabel` immediately after `DryRunSummaryLabel`:

```go
DryRunLabel        = KratixPrefix + "dry-run"
DryRunSummaryLabel = KratixPrefix + "dry-run-summary"
DryRunOwnerLabel   = KratixPrefix + "dry-run-owner"
```

- [ ] **Step 2: Build to confirm no breakage**

```bash
go build ./...
```

Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add api/v1alpha1/work_types.go
git commit -m "feat(dry-run): add DryRunOwnerLabel constant"
```

---

## Task 2: DryRun CRD types

**Files:**
- Create: `api/v1alpha1/dryrun_types.go`
- Modify: `api/v1alpha1/zz_generated.deepcopy.go`

- [ ] **Step 1: Create `api/v1alpha1/dryrun_types.go`**

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:categories=kratix

// DryRun previews the output of a Kratix pipeline without applying it to a real Destination.
type DryRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DryRunSpec   `json:"spec,omitempty"`
	Status DryRunStatus `json:"status,omitempty"`
}

// DryRunSpec defines the desired state of DryRun.
type DryRunSpec struct {
	// PromiseRef is the name of the Promise whose pipeline to dry-run.
	PromiseRef DryRunPromiseRef `json:"promiseRef"`
	// ResourceRequestRef identifies the live ResourceRequest to diff against.
	// When the referenced object is not found the diff treats the request as new (all files added).
	ResourceRequestRef DryRunResourceRequestRef `json:"resourceRequestRef"`
	// Resource is the spec to dry-run, in the shape expected by the Promise's resource API.
	Resource runtime.RawExtension `json:"resource"`
}

// DryRunPromiseRef names a Promise.
type DryRunPromiseRef struct {
	Name string `json:"name"`
}

// DryRunResourceRequestRef names the live ResourceRequest to diff against.
type DryRunResourceRequestRef struct {
	Name string `json:"name"`
	// Namespace of the live ResourceRequest. Defaults to the DryRun's own namespace when omitted.
	Namespace string `json:"namespace,omitempty"`
}

// DryRunStatus defines the observed state of DryRun.
type DryRunStatus struct {
	// Conditions holds status conditions. The "Completed" condition is True once the summary has been written.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true

// DryRunList contains a list of DryRun.
type DryRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DryRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DryRun{}, &DryRunList{})
}
```

- [ ] **Step 2: Add DeepCopy methods to `api/v1alpha1/zz_generated.deepcopy.go`**

Append the following block at the end of the file (before the final newline):

```go
// DeepCopyInto copies all properties of this object into another object of the same type.
func (in *DryRun) DeepCopyInto(out *DryRun) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *DryRun) DeepCopy() *DryRun {
	if in == nil {
		return nil
	}
	out := new(DryRun)
	in.DeepCopyInto(out)
	return out
}

func (in *DryRun) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DryRunList) DeepCopyInto(out *DryRunList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]DryRun, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *DryRunList) DeepCopy() *DryRunList {
	if in == nil {
		return nil
	}
	out := new(DryRunList)
	in.DeepCopyInto(out)
	return out
}

func (in *DryRunList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *DryRunSpec) DeepCopyInto(out *DryRunSpec) {
	*out = *in
	out.PromiseRef = in.PromiseRef
	out.ResourceRequestRef = in.ResourceRequestRef
	in.Resource.DeepCopyInto(&out.Resource)
}

func (in *DryRunSpec) DeepCopy() *DryRunSpec {
	if in == nil {
		return nil
	}
	out := new(DryRunSpec)
	in.DeepCopyInto(out)
	return out
}

func (in *DryRunStatus) DeepCopyInto(out *DryRunStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *DryRunStatus) DeepCopy() *DryRunStatus {
	if in == nil {
		return nil
	}
	out := new(DryRunStatus)
	in.DeepCopyInto(out)
	return out
}

func (in *DryRunPromiseRef) DeepCopyInto(out *DryRunPromiseRef) { *out = *in }

func (in *DryRunPromiseRef) DeepCopy() *DryRunPromiseRef {
	if in == nil {
		return nil
	}
	out := new(DryRunPromiseRef)
	in.DeepCopyInto(out)
	return out
}

func (in *DryRunResourceRequestRef) DeepCopyInto(out *DryRunResourceRequestRef) { *out = *in }

func (in *DryRunResourceRequestRef) DeepCopy() *DryRunResourceRequestRef {
	if in == nil {
		return nil
	}
	out := new(DryRunResourceRequestRef)
	in.DeepCopyInto(out)
	return out
}
```

- [ ] **Step 3: Build to confirm**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add api/v1alpha1/dryrun_types.go api/v1alpha1/zz_generated.deepcopy.go
git commit -m "feat(dry-run): add DryRun CRD types"
```

---

## Task 3: DryRun controller

**Files:**
- Create: `internal/controller/dryrun_controller.go`
- Modify: `cmd/main.go`

This is the core of the feature. The controller:
1. Creates an ephemeral ResourceRequest carrying `DryRunLabel` and `DryRunOwnerLabel`
2. Polls until DRRC sets `WorksSucceeded=True` on the ephemeral RR
3. Finds dry-run Works (by ephemeral RR name) and live Works (by `resourceRequestRef`)
4. Diffs them per pipeline using the diff logic moved from work-creator
5. Writes a summary Work and updates DryRun status

- [ ] **Step 1: Create `internal/controller/dryrun_controller.go`**

```go
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/objectutil"
	"github.com/syntasso/kratix/lib/resourceutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const dryRunCompletedCondition = "Completed"

type DryRunReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

func (r *DryRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("dryrun", req.NamespacedName)

	dryRun := &v1alpha1.DryRun{}
	if err := r.Client.Get(ctx, req.NamespacedName, dryRun); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if isDryRunCompleted(dryRun) {
		return ctrl.Result{}, nil
	}

	// Fetch the Promise to derive the resource GVK.
	promise := &v1alpha1.Promise{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: dryRun.Spec.PromiseRef.Name}, promise); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching promise %q: %w", dryRun.Spec.PromiseRef.Name, err)
	}

	gvk, _, err := promise.GetAPI()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting GVK from promise %q: %w", promise.Name, err)
	}

	ephemeralName := objectutil.GenerateDeterministicObjectName("kratix-dry-run-" + dryRun.Name)

	if err := r.ensureEphemeralRR(ctx, dryRun, gvk, ephemeralName); err != nil {
		return ctrl.Result{}, err
	}

	// Poll the ephemeral RR's WorksSucceeded condition.
	rrObj := &unstructured.Unstructured{}
	rrObj.SetGroupVersionKind(*gvk)
	if err := r.Client.Get(ctx, types.NamespacedName{Name: ephemeralName, Namespace: dryRun.Namespace}, rrObj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	worksSucceeded := resourceutil.GetCondition(rrObj, resourceutil.WorksSucceededCondition)
	if worksSucceeded == nil || worksSucceeded.Status != corev1.ConditionTrue {
		logger.Info("waiting for ephemeral RR WorksSucceeded", "ephemeralRR", ephemeralName)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	namespace := dryRun.Namespace
	if namespace == "" {
		namespace = v1alpha1.SystemNamespace
	}

	if err := r.writeSummary(ctx, logger, dryRun, promise.GetName(), ephemeralName, namespace); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.markDryRunCompleted(ctx, dryRun)
}

func (r *DryRunReconciler) ensureEphemeralRR(
	ctx context.Context,
	dryRun *v1alpha1.DryRun,
	gvk *schema.GroupVersionKind,
	ephemeralName string,
) error {
	rrObj := &unstructured.Unstructured{}
	rrObj.SetGroupVersionKind(*gvk)
	rrObj.SetName(ephemeralName)
	rrObj.SetNamespace(dryRun.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rrObj, func() error {
		rrObj.SetLabels(map[string]string{
			v1alpha1.DryRunLabel:      "true",
			v1alpha1.DryRunOwnerLabel: dryRun.Name,
		})
		if err := controllerutil.SetControllerReference(dryRun, rrObj, r.Scheme); err != nil {
			return err
		}
		spec := map[string]interface{}{}
		if err := json.Unmarshal(dryRun.Spec.Resource.Raw, &spec); err != nil {
			return fmt.Errorf("unmarshaling resource spec: %w", err)
		}
		rrObj.Object["spec"] = spec
		return nil
	})
	return err
}

func (r *DryRunReconciler) writeSummary(
	ctx context.Context,
	logger logr.Logger,
	dryRun *v1alpha1.DryRun,
	promiseName, ephemeralName, namespace string,
) error {
	// Dry-run Works produced by this DryRun's pipeline.
	dryWorkList := &v1alpha1.WorkList{}
	if err := r.Client.List(ctx, dryWorkList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			v1alpha1.PromiseNameLabel:  promiseName,
			v1alpha1.ResourceNameLabel: ephemeralName,
			v1alpha1.DryRunLabel:       "true",
		}),
		Namespace: namespace,
	}); err != nil {
		return err
	}

	// Live Works for the referenced ResourceRequest (empty map when not found → new request).
	liveWorksByPipeline := map[string]*v1alpha1.Work{}
	ref := dryRun.Spec.ResourceRequestRef
	if ref.Name != "" {
		liveNS := ref.Namespace
		if liveNS == "" {
			liveNS = dryRun.Namespace
		}
		liveSelector := labels.Set{
			v1alpha1.PromiseNameLabel:  promiseName,
			v1alpha1.ResourceNameLabel: ref.Name,
		}
		if ref.Namespace != "" {
			liveSelector[v1alpha1.ResourceNamespaceLabel] = ref.Namespace
		}
		liveWorkList := &v1alpha1.WorkList{}
		if err := r.Client.List(ctx, liveWorkList, &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(liveSelector),
			Namespace:     liveNS,
		}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		for i := range liveWorkList.Items {
			pipelineName := liveWorkList.Items[i].GetLabels()[v1alpha1.PipelineNameLabel]
			liveWorksByPipeline[pipelineName] = &liveWorkList.Items[i]
		}
	}

	type section struct {
		pipeline string
		content  string
	}
	var sections []section

	for i := range dryWorkList.Items {
		dryWork := &dryWorkList.Items[i]
		if dryWork.GetLabels()[v1alpha1.DryRunSummaryLabel] == "true" {
			continue
		}
		pipelineName := dryWork.GetLabels()[v1alpha1.PipelineNameLabel]

		dryFiles, err := dryRunExtractWorkFiles(dryWork)
		if err != nil {
			return fmt.Errorf("extracting dry-run work files for pipeline %q: %w", pipelineName, err)
		}
		liveFiles := map[string]string{}
		if liveWork, ok := liveWorksByPipeline[pipelineName]; ok {
			liveFiles, err = dryRunExtractWorkFiles(liveWork)
			if err != nil {
				return fmt.Errorf("extracting live work files for pipeline %q: %w", pipelineName, err)
			}
		}

		sections = append(sections, section{pipeline: pipelineName, content: dryRunRenderDiff(liveFiles, dryFiles)})
	}

	if len(sections) == 0 {
		return nil
	}

	sort.Slice(sections, func(i, j int) bool { return sections[i].pipeline < sections[j].pipeline })

	var sb strings.Builder
	sb.WriteString("# Kratix Dry Run Summary\n\n")
	for i, s := range sections {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		fmt.Fprintf(&sb, "## Pipeline: `%s`\n\n%s", s.pipeline, s.content)
	}

	compressed, err := compression.CompressContent([]byte(sb.String()))
	if err != nil {
		return fmt.Errorf("compressing dry-run summary: %w", err)
	}

	summaryWork := &v1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectutil.GenerateDeterministicObjectName(fmt.Sprintf("%s-%s-dry-run-summary", promiseName, dryRun.Name)),
			Namespace: namespace,
		},
	}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, summaryWork, func() error {
		summaryWork.Labels = map[string]string{
			v1alpha1.PromiseNameLabel:   promiseName,
			v1alpha1.ResourceNameLabel:  ephemeralName,
			v1alpha1.WorkTypeLabel:      string(v1alpha1.WorkflowTypeResource),
			v1alpha1.DryRunLabel:        "true",
			v1alpha1.DryRunSummaryLabel: "true",
			v1alpha1.DryRunOwnerLabel:   dryRun.Name,
		}
		summaryWork.Spec = v1alpha1.WorkSpec{
			PromiseName:  promiseName,
			ResourceName: ephemeralName,
			WorkloadGroups: []v1alpha1.WorkloadGroup{{
				ID:        hash.ComputeHash("dry-run-summary"),
				Directory: v1alpha1.DefaultWorkloadGroupDirectory,
				Workloads: []v1alpha1.Workload{{
					Filepath: "kratix-dry-run-summary.md",
					Content:  string(compressed),
				}},
			}},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("upserting dry-run summary work: %w", err)
	}
	logger.Info("dry-run summary written", "pipelines", len(sections))
	return nil
}

func (r *DryRunReconciler) markDryRunCompleted(ctx context.Context, dryRun *v1alpha1.DryRun) error {
	apiMeta.SetStatusCondition(&dryRun.Status.Conditions, metav1.Condition{
		Type:               dryRunCompletedCondition,
		Status:             metav1.ConditionTrue,
		Reason:             "SummaryWritten",
		Message:            "Dry run completed; summary written to output repository",
		LastTransitionTime: metav1.Now(),
	})
	return r.Client.Status().Update(ctx, dryRun)
}

func isDryRunCompleted(dryRun *v1alpha1.DryRun) bool {
	for _, c := range dryRun.Status.Conditions {
		if c.Type == dryRunCompletedCondition && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// SetupWithManager registers the DryRunReconciler with the controller-runtime manager.
func (r *DryRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DryRun{}).
		Complete(r)
}

// --- diff helpers (moved from work-creator/lib/work_creator.go) ---

func dryRunExtractWorkFiles(work *v1alpha1.Work) (map[string]string, error) {
	files := map[string]string{}
	if work == nil {
		return files, nil
	}
	for _, group := range work.Spec.WorkloadGroups {
		for _, wl := range group.Workloads {
			content, err := compression.DecompressContent([]byte(wl.Content))
			if err != nil {
				return nil, fmt.Errorf("decompressing %s: %w", wl.Filepath, err)
			}
			files[wl.Filepath] = string(content)
		}
	}
	return files, nil
}

func dryRunRenderDiff(prev, next map[string]string) string {
	var added, removed, modified []string
	for path := range next {
		if _, exists := prev[path]; !exists {
			added = append(added, path)
		} else if prev[path] != next[path] {
			modified = append(modified, path)
		}
	}
	for path := range prev {
		if _, exists := next[path]; !exists {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)

	var b strings.Builder
	fmt.Fprintf(&b, "# Kratix Dry Run Diff\n\n")
	fmt.Fprintf(&b, "**%d added · %d modified · %d removed**\n", len(added), len(modified), len(removed))

	if len(added)+len(modified)+len(removed) == 0 {
		b.WriteString("\nNo changes.\n")
		return b.String()
	}

	for _, path := range added {
		fmt.Fprintf(&b, "\n---\n\n## ➕ Added: `%s`\n\n```yaml\n%s\n```\n", path, strings.TrimRight(next[path], "\n"))
	}
	for _, path := range modified {
		fmt.Fprintf(&b, "\n---\n\n## ✏️ Modified: `%s`\n\n```diff\n%s```\n", path, dryRunLineDiff(prev[path], next[path]))
	}
	for _, path := range removed {
		fmt.Fprintf(&b, "\n---\n\n## 🗑️ Removed: `%s`\n\n```yaml\n%s\n```\n", path, strings.TrimRight(prev[path], "\n"))
	}

	return b.String()
}

func dryRunLineDiff(old, new string) string {
	dmp := diffmatchpatch.New()
	a, b, lineArray := dmp.DiffLinesToChars(old, new)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	var sb strings.Builder
	for _, d := range diffs {
		lines := strings.Split(strings.TrimSuffix(d.Text, "\n"), "\n")
		prefix := " "
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			prefix = "+"
		case diffmatchpatch.DiffDelete:
			prefix = "-"
		}
		for _, line := range lines {
			fmt.Fprintf(&sb, "%s %s\n", prefix, line)
		}
	}
	return sb.String()
}
```

**Note on `schema` import:** `schema` is used in `ensureEphemeralRR`'s parameter type `*schema.GroupVersionKind`. Add `"k8s.io/apimachinery/pkg/runtime/schema"` to the import block.

- [ ] **Step 2: Register the controller in `cmd/main.go`**

Find the `//+kubebuilder:scaffold:builder` comment (around line 422). Add the registration immediately before it:

```go
if err := (&controller.DryRunReconciler{
    Client: mgr.GetClient(),
    Scheme: mgr.GetScheme(),
    Log:    ctrl.Log.WithName("controllers").WithName("DryRunController"),
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "unable to create controller", "controller", "DryRun")
    os.Exit(1)
}
```

- [ ] **Step 3: Build to confirm**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/dryrun_controller.go cmd/main.go
git commit -m "feat(dry-run): add DryRunReconciler with ephemeral RR, diff, and summary"
```

---

## Task 4: Skip `ensureDryRunSummary` for DryRun-owned ephemeral RRs

The DRRC must not call `ensureDryRunSummary` for ephemeral RRs that were created by the DryRunReconciler — the DryRunReconciler owns that responsibility for them.

**Files:**
- Modify: `internal/controller/dynamic_resource_request_controller.go:429-440`

- [ ] **Step 1: Add the owner-label guard**

Find this block in `ensureResourceStatus` (around line 429):

```go
if rr.GetLabels()[v1alpha1.DryRunLabel] == "true" {
    worksSucceeded := resourceutil.GetCondition(rr, resourceutil.WorksSucceededCondition)
    if worksSucceeded != nil && worksSucceeded.Status == v1.ConditionTrue {
        namespace := rr.GetNamespace()
        if namespace == "" {
            namespace = v1alpha1.SystemNamespace
        }
        if err := r.ensureDryRunSummary(ctx, logger, rr, namespace); err != nil {
            return false, err
        }
    }
}
```

Replace with:

```go
// Skip summary generation for ephemeral RRs owned by a DryRun object — DryRunReconciler handles those.
if rr.GetLabels()[v1alpha1.DryRunLabel] == "true" && rr.GetLabels()[v1alpha1.DryRunOwnerLabel] == "" {
    worksSucceeded := resourceutil.GetCondition(rr, resourceutil.WorksSucceededCondition)
    if worksSucceeded != nil && worksSucceeded.Status == v1.ConditionTrue {
        namespace := rr.GetNamespace()
        if namespace == "" {
            namespace = v1alpha1.SystemNamespace
        }
        if err := r.ensureDryRunSummary(ctx, logger, rr, namespace); err != nil {
            return false, err
        }
    }
}
```

- [ ] **Step 2: Build to confirm**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add internal/controller/dynamic_resource_request_controller.go
git commit -m "fix(dry-run): skip ensureDryRunSummary for DryRun-controller-owned ephemeral RRs"
```

---

## Task 5: Remove diff logic from work-creator

The diff now lives in the DryRun controller. Remove it from work-creator.

**Files:**
- Modify: `work-creator/lib/work_creator.go`

- [ ] **Step 1: Remove the dry-run diff block from `Execute`**

In `work_creator.go`, find and delete this block (around lines 121-130):

```go
if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
    liveLabels := resourceutil.GetWorkLabels(promiseName, resourceName, resourceNamespace, pipelineName, workflowType)
    liveWork, err := resourceutil.GetWork(w.K8sClient, namespace, liveLabels)
    if err != nil {
        return err
    }
    if err := w.writeDryRunDiff(pipelineOutputDir, liveWork); err != nil {
        return err
    }
}
```

- [ ] **Step 2: Remove the diff helper functions**

Delete the following functions entirely from `work_creator.go` (they start around line 426):

- `writeDryRunDiff` (lines ~430-443)
- `extractWorkFiles` (lines ~445-462)
- `readDirFiles` (lines ~464-484)
- `renderDiff` (lines ~486-528)
- `lineDiff` (lines ~530-553)

- [ ] **Step 3: Remove unused imports**

From the import block at the top of `work_creator.go`, remove:

```go
"sort"
"github.com/sergi/go-diff/diffmatchpatch"
```

(`sort` was only used in `renderDiff`; `diffmatchpatch` was only used in `lineDiff`.)

- [ ] **Step 4: Build to confirm**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add work-creator/lib/work_creator.go
git commit -m "refactor(dry-run): remove diff logic from work-creator; moved to DryRunReconciler"
```

---

## Task 6: Update the GitHub Actions agent

The agent now commits a `DryRun` object instead of a labelled ResourceRequest.

**Files:**
- Modify: `docs/github-actions/dry-run/action.yml`

The key changes are:
- Step "Stage dry-run ResourceRequests" → builds and commits a `DryRun` YAML (not a labelled RR)
- Step "Remove dry-run ResourceRequests" → removes the same directory (name unchanged, logic identical)

The `spec.promiseRef.name` is derived from the RR's `kind` lowercased — e.g. `kind: Redis` → `promiseRef.name: redis`. This convention holds for standard Kratix promises. If a promise name differs from the lowercased kind, the user should override it with an annotation `kratix.io/promise-name` on the RR YAML.

- [ ] **Step 1: Replace the staging step**

Find the step named `Stage dry-run ResourceRequests` (the `run:` block starts at around line 96). Replace the entire `run:` block with:

```bash
dry_run_dir=".kratix-dry-runs/pr-${PR_NUMBER}"

git clone \
  "https://x-access-token:${GITOPS_TOKEN}@github.com/${REPO}.git" \
  /tmp/gitops-repo
cd /tmp/gitops-repo
git checkout "$GITOPS_BRANCH"

rm -rf "$dry_run_dir"
mkdir -p "$dry_run_dir"

for f in $RR_FILES; do
  src="${WORKSPACE}/${f}"
  dest="${dry_run_dir}/$(basename "$f")"
  python3 - "$src" "$dest" "$PR_NUMBER" <<'PYEOF'
import sys, yaml

src, dest, pr_number = sys.argv[1], sys.argv[2], sys.argv[3]
with open(src) as fh:
    doc = yaml.safe_load(fh)

rr_name      = doc['metadata']['name']
rr_namespace = doc['metadata'].get('namespace', 'default')
# Convention: promise name = lowercase Kind.
# Override with annotation kratix.io/promise-name if the promise is named differently.
promise_name = doc['metadata'].get('annotations', {}).get(
    'kratix.io/promise-name', doc['kind'].lower()
)
spec = doc.get('spec', {})

dry_run = {
    'apiVersion': 'platform.kratix.io/v1alpha1',
    'kind': 'DryRun',
    'metadata': {
        'name': f"{rr_name}-pr-{pr_number}",
        'namespace': rr_namespace,
    },
    'spec': {
        'promiseRef': {'name': promise_name},
        'resourceRequestRef': {'name': rr_name, 'namespace': rr_namespace},
        'resource': spec,
    },
}

with open(dest, 'w') as fh:
    yaml.dump(dry_run, fh, default_flow_style=False)
PYEOF
done

git config user.email "kratix-dry-run[bot]@users.noreply.github.com"
git config user.name "kratix-dry-run[bot]"
git add "$dry_run_dir"
git commit -m "chore: stage dry-run for PR #${PR_NUMBER} [skip ci]"
git push origin "$GITOPS_BRANCH"
```

- [ ] **Step 2: Build to confirm (linting)**

The action is a shell/Python script — confirm the YAML is valid:

```bash
python3 -c "import yaml; yaml.safe_load(open('docs/github-actions/dry-run/action.yml'))" && echo "OK"
```

Expected: `OK`

- [ ] **Step 3: Commit**

```bash
git add docs/github-actions/dry-run/action.yml
git commit -m "feat(dry-run): update CI agent to commit DryRun objects instead of labelled RRs"
```

---

## Self-Review

**Spec coverage:**

| Requirement | Task |
|---|---|
| DryRun CRD with promiseRef, resourceRequestRef, resource | Task 2 |
| resourceRequestRef not found → treat as new request | Task 3: `liveWorksByPipeline` empty map when RR not found |
| resourceRequestRef.namespace defaults to DryRun's namespace | Task 3: `liveNS := ref.Namespace; if liveNS == "" { liveNS = dryRun.Namespace }` |
| Ephemeral RR owned by DryRun (cascade delete) | Task 3: `controllerutil.SetControllerReference` |
| DRRC skips ensureDryRunSummary for DryRun-owned RRs | Task 4 |
| Diff logic removed from work-creator | Task 5 |
| CI agent commits DryRun YAML | Task 6 |

**Placeholder scan:** None found.

**Type consistency:** `DryRunOwnerLabel` (Task 1) is used identically in Task 3 (controller) and Task 4 (DRRC guard). `dryRunCompletedCondition = "Completed"` is defined once in dryrun_controller.go and used in both `markDryRunCompleted` and `isDryRunCompleted`. `DryRunPromiseRef` and `DryRunResourceRequestRef` defined in Task 2 and referenced in Task 3.

**One gap to note:** The `schema` import in `dryrun_controller.go` must include `"k8s.io/apimachinery/pkg/runtime/schema"` — called out in the Step 1 note of Task 3. The full import block should be:

```go
import (
    "context"
    "encoding/json"
    "fmt"
    "sort"
    "strings"
    "time"

    "github.com/go-logr/logr"
    "github.com/sergi/go-diff/diffmatchpatch"
    "github.com/syntasso/kratix/api/v1alpha1"
    "github.com/syntasso/kratix/lib/compression"
    "github.com/syntasso/kratix/lib/hash"
    "github.com/syntasso/kratix/lib/objectutil"
    "github.com/syntasso/kratix/lib/resourceutil"
    corev1 "k8s.io/api/core/v1"
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    apiMeta "k8s.io/apimachinery/pkg/api/meta"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/labels"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/apimachinery/pkg/types"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)
```
