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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		return ctrl.Result{}, fmt.Errorf("ensuring ephemeral RR: %w", err)
	}

	rrObj := &unstructured.Unstructured{}
	rrObj.SetGroupVersionKind(*gvk)
	if err := r.Client.Get(ctx, types.NamespacedName{Name: ephemeralName, Namespace: dryRun.Namespace}, rrObj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	worksSucceeded := resourceutil.GetCondition(rrObj, resourceutil.WorksSucceededCondition)
	if worksSucceeded != nil && worksSucceeded.Status == corev1.ConditionFalse {
		return ctrl.Result{}, r.markDryRunFailed(ctx, dryRun, "pipeline failed: "+worksSucceeded.Message)
	}
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
		existing := rrObj.GetLabels()
		if existing == nil {
			existing = map[string]string{}
		}
		existing[v1alpha1.DryRunLabel] = "true"
		existing[v1alpha1.DryRunOwnerLabel] = dryRun.Name
		rrObj.SetLabels(existing)
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
		}); err != nil {
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

func (r *DryRunReconciler) markDryRunFailed(ctx context.Context, dryRun *v1alpha1.DryRun, message string) error {
	apiMeta.SetStatusCondition(&dryRun.Status.Conditions, metav1.Condition{
		Type:               dryRunCompletedCondition,
		Status:             metav1.ConditionFalse,
		Reason:             "PipelineFailed",
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
	return r.Client.Status().Update(ctx, dryRun)
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
		if c.Type == dryRunCompletedCondition {
			return true
		}
	}
	return false
}

func (r *DryRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.DryRun{}).
		Complete(r)
}

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

func dryRunLineDiff(old, updated string) string {
	dmp := diffmatchpatch.New()
	a, b, lineArray := dmp.DiffLinesToChars(old, updated)
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
