package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
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
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

//+kubebuilder:rbac:groups=platform.kratix.io,resources=dryruns,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=dryruns/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=dryruns/finalizers,verbs=update

const dryRunCompletedCondition = "Completed"

// dryRunComponentsSucceededCondition reports whether a compound request's preview is
// COMPLETE. Kept separate from Completed, which only says the run finished -- a
// compound run that wrote a partial summary is Completed=True but not complete.
const dryRunComponentsSucceededCondition = "ComponentsSucceeded"

// componentDryRunTimeout bounds how long a compound dry run waits for its
// component dry runs before writing a partial summary. A component whose
// pipeline crash-loops never reports a condition at all, so without a bound the
// compound dry run would wait forever.
const componentDryRunTimeout = 10 * time.Minute

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

	// A failed pipeline writes no Works at all, so WorksSucceeded never appears -- it is
	// absent rather than False. Without this check the dry run waits forever on a
	// condition that is never coming. ConfigureWorkflowCompleted is where Kratix
	// actually reports the failure.
	configureCompleted := resourceutil.GetCondition(rrObj, resourceutil.ConfigureWorkflowCompletedCondition)
	if configureCompleted != nil && configureCompleted.Status == corev1.ConditionFalse &&
		configureCompleted.Reason == resourceutil.ConfigureWorkflowCompletedFailedReason {
		return ctrl.Result{}, r.markDryRunFailed(ctx, dryRun, configureCompleted.Message)
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

	// Compound Promises: the pipeline output may contain component resource
	// requests, labelled by the compound workflow. Raise a DryRun for each so the
	// preview covers what the components would change, not just the request YAML.
	expected, err := r.ensureComponentDryRuns(ctx, logger, dryRun, promise.GetName(), ephemeralName, namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring component dry runs: %w", err)
	}

	children, err := r.componentDryRuns(ctx, dryRun, namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// The client is cache-backed, so a component DryRun created moments ago may not
	// be listed yet. Without this the compound run would see zero components and
	// write a summary with no component sections at all.
	if len(children) < len(expected) {
		logger.Info("waiting for component dry runs to appear in cache",
			"expected", len(expected), "listed", len(children))
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Surface the component tree in status while the preview is still running, so a
	// consumer can see its shape and progress without parsing the summary markdown.
	if err := r.syncComponentStatus(ctx, dryRun, children); err != nil {
		return ctrl.Result{}, err
	}

	if pending := pendingComponentDryRuns(children); len(pending) > 0 {
		if time.Since(dryRun.CreationTimestamp.Time) < componentDryRunTimeout {
			logger.Info("waiting for component dry runs", "pending", pending)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		// Partial results beat hanging: report what completed and flag the rest.
		logger.Info("component dry run timeout; writing partial summary", "pending", pending)
	}

	if err := r.writeSummary(ctx, logger, dryRun, promise.GetName(), ephemeralName, namespace, children); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.markDryRunCompleted(ctx, dryRun, children)
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

		// Annotate with real resource coordinates so the reader can fetch it
		// and give the pipeline correct metadata (name, namespace, labels, etc.).
		ref := dryRun.Spec.ResourceRequestRef
		if ref.Name != "" {
			annotations := rrObj.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			realNamespace := ref.Namespace
			if realNamespace == "" {
				realNamespace = dryRun.Namespace
			}
			annotations[v1alpha1.DryRunResourceNameAnnotation] = ref.Name
			annotations[v1alpha1.DryRunResourceNamespaceAnnotation] = realNamespace
			rrObj.SetAnnotations(annotations)
		}

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

// componentRequests returns the component resource requests found in the compound
// Promise's dry-run output. A document is a component request if it carries
// ParentResourceNameLabel -- the label the compound workflow stamps on the
// requests it emits. Kratix has no other way to tell a component request apart
// from an ordinary workload, so this label is the whole contract.
func (r *DryRunReconciler) componentRequests(
	ctx context.Context,
	promiseName, ephemeralName, namespace string,
) ([]*unstructured.Unstructured, error) {
	workList := &v1alpha1.WorkList{}
	if err := r.Client.List(ctx, workList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			v1alpha1.PromiseNameLabel:  promiseName,
			v1alpha1.ResourceNameLabel: ephemeralName,
			v1alpha1.DryRunLabel:       "true",
		}),
		Namespace: namespace,
	}); err != nil {
		return nil, err
	}

	var requests []*unstructured.Unstructured
	for i := range workList.Items {
		work := &workList.Items[i]
		if work.GetLabels()[v1alpha1.DryRunSummaryLabel] == "true" {
			continue
		}
		files, err := dryRunExtractWorkFiles(work)
		if err != nil {
			return nil, err
		}
		for _, path := range sortedKeys(files) {
			docs, err := decodeYAMLDocs(files[path])
			if err != nil {
				// A non-Kubernetes file in the output is normal; skip it rather than
				// failing the whole dry run.
				continue
			}
			for _, doc := range docs {
				if doc.GetLabels()[v1alpha1.ParentResourceNameLabel] == "" {
					continue
				}
				requests = append(requests, doc)
			}
		}
	}
	return requests, nil
}

// ensureComponentDryRuns creates one DryRun per component request found in the
// compound Promise's output, owned by the compound DryRun so they cascade-delete.
// It returns the names it ensured, so the caller can tell the difference between
// "no components" and "components not visible in the cache yet".
func (r *DryRunReconciler) ensureComponentDryRuns(
	ctx context.Context,
	logger logr.Logger,
	dryRun *v1alpha1.DryRun,
	promiseName, ephemeralName, namespace string,
) ([]string, error) {
	requests, err := r.componentRequests(ctx, promiseName, ephemeralName, namespace)
	if err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, nil
	}

	promisesByGVK, err := r.promisesByGVK(ctx)
	if err != nil {
		return nil, err
	}

	var ensured []string
	for _, req := range requests {
		gvk := req.GroupVersionKind().String()
		componentPromise, ok := promisesByGVK[gvk]
		if !ok {
			// The component's Promise isn't installed, so there is no pipeline to
			// preview. Skip rather than fail; the request still shows in the diff as a
			// file, which is the best available answer.
			logger.Info("no installed Promise serves component request; skipping",
				"gvk", gvk, "name", req.GetName())
			continue
		}

		componentNamespace := req.GetNamespace()
		if componentNamespace == "" {
			componentNamespace = namespace
		}

		spec, _, err := unstructured.NestedMap(req.Object, "spec")
		if err != nil {
			return nil, fmt.Errorf("reading spec of component request %s: %w", req.GetName(), err)
		}
		rawSpec, err := json.Marshal(spec)
		if err != nil {
			return nil, fmt.Errorf("marshaling spec of component request %s: %w", req.GetName(), err)
		}

		child := &v1alpha1.DryRun{
			ObjectMeta: metav1.ObjectMeta{
				Name: objectutil.GenerateDeterministicObjectName(
					fmt.Sprintf("%s-%s-%s", dryRun.Name, componentPromise, req.GetName())),
				Namespace: dryRun.Namespace,
			},
		}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, child, func() error {
			if child.Labels == nil {
				child.Labels = map[string]string{}
			}
			child.Labels[v1alpha1.DryRunParentLabel] = dryRun.Name
			child.Labels[v1alpha1.PromiseNameLabel] = componentPromise
			if err := controllerutil.SetControllerReference(dryRun, child, r.Scheme); err != nil {
				return err
			}
			child.Spec = v1alpha1.DryRunSpec{
				PromiseRef: v1alpha1.DryRunPromiseRef{Name: componentPromise},
				ResourceRequestRef: v1alpha1.DryRunResourceRequestRef{
					Name:      req.GetName(),
					Namespace: componentNamespace,
				},
				Resource: runtime.RawExtension{Raw: rawSpec},
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("upserting component dry run for %s: %w", req.GetName(), err)
		}
		ensured = append(ensured, child.Name)
	}
	return ensured, nil
}

// componentDryRuns lists the DryRuns raised by this compound DryRun.
func (r *DryRunReconciler) componentDryRuns(
	ctx context.Context, dryRun *v1alpha1.DryRun, namespace string,
) ([]v1alpha1.DryRun, error) {
	list := &v1alpha1.DryRunList{}
	if err := r.Client.List(ctx, list, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{v1alpha1.DryRunParentLabel: dryRun.Name}),
		Namespace:     namespace,
	}); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items, nil
}

func pendingComponentDryRuns(children []v1alpha1.DryRun) []string {
	var pending []string
	for i := range children {
		if !isDryRunCompleted(&children[i]) {
			pending = append(pending, children[i].Name)
		}
	}
	return pending
}

// promisesByGVK maps each installed Promise's resource GVK to its Promise name, so
// a component request can be matched to the Promise that serves it.
func (r *DryRunReconciler) promisesByGVK(ctx context.Context) (map[string]string, error) {
	promiseList := &v1alpha1.PromiseList{}
	if err := r.Client.List(ctx, promiseList); err != nil {
		return nil, err
	}
	byGVK := map[string]string{}
	for i := range promiseList.Items {
		gvk, _, err := promiseList.Items[i].GetAPI()
		if err != nil || gvk == nil {
			continue
		}
		byGVK[gvk.String()] = promiseList.Items[i].GetName()
	}
	return byGVK, nil
}

func decodeYAMLDocs(content string) ([]*unstructured.Unstructured, error) {
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(content), 2048)
	var docs []*unstructured.Unstructured
	for {
		doc := &unstructured.Unstructured{}
		err := decoder.Decode(doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return docs, err
		}
		if len(doc.Object) == 0 || doc.GetKind() == "" {
			continue
		}
		docs = append(docs, doc)
	}
}

// componentSummaryBody returns the diff a component DryRun produced, ready to nest
// under a component heading. Components that failed or never finished report their
// state instead of a diff, so a partial summary is explicit about what is missing
// rather than silently omitting it.
func (r *DryRunReconciler) componentSummaryBody(
	ctx context.Context, child *v1alpha1.DryRun, namespace string,
) (string, error) {
	for _, c := range child.Status.Conditions {
		if c.Type == dryRunCompletedCondition && c.Status == metav1.ConditionFalse {
			return fmt.Sprintf("> **Dry run failed.** %s\n", c.Message), nil
		}
	}
	if !isDryRunCompleted(child) {
		return "> **Dry run did not complete** within the timeout. " +
			"No diff available for this component.\n", nil
	}

	workList := &v1alpha1.WorkList{}
	if err := r.Client.List(ctx, workList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			v1alpha1.DryRunSummaryLabel: "true",
			v1alpha1.DryRunOwnerLabel:   child.Name,
		}),
		Namespace: namespace,
	}); err != nil {
		return "", err
	}
	if len(workList.Items) == 0 {
		return "> No changes.\n", nil
	}

	files, err := dryRunExtractWorkFiles(&workList.Items[0])
	if err != nil {
		return "", err
	}
	for _, path := range sortedKeys(files) {
		body := files[path]
		body = strings.TrimPrefix(body, "# Kratix Dry Run Summary\n\n")
		// Nest the component's pipeline headings under the component heading.
		body = strings.ReplaceAll(body, "## Pipeline: ", "### Pipeline: ")
		return body, nil
	}
	return "> No changes.\n", nil
}

// dryRunSummaryFilepath keeps the top-level summary at a stable, predictable path
// while giving component summaries distinct ones. On a Destination using
// filepath.mode: none every summary would otherwise land on the same file and the
// components would clobber the aggregate.
func dryRunSummaryFilepath(dryRun *v1alpha1.DryRun) string {
	if parent := dryRun.GetLabels()[v1alpha1.DryRunParentLabel]; parent != "" {
		return fmt.Sprintf("kratix-dry-run-summary-%s.md", dryRun.Name)
	}
	return "kratix-dry-run-summary.md"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (r *DryRunReconciler) writeSummary(
	ctx context.Context,
	logger logr.Logger,
	dryRun *v1alpha1.DryRun,
	promiseName, ephemeralName, namespace string,
	children []v1alpha1.DryRun,
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
		// ResourceNamespaceLabel is only set on Works when WorkflowPipelineNamespaceSet;
		// don't require it in the selector — the Namespace ListOption is sufficient.
		liveSelector := labels.Set{
			v1alpha1.PromiseNameLabel:  promiseName,
			v1alpha1.ResourceNameLabel: ref.Name,
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

	if len(sections) == 0 && len(children) == 0 {
		return nil
	}

	sort.Slice(sections, func(i, j int) bool { return sections[i].pipeline < sections[j].pipeline })

	var sb strings.Builder
	sb.WriteString("# Kratix Dry Run Summary\n\n")

	// Without components this is a plain resource dry run: keep the output byte
	// identical to what it has always been. Component sections only appear, and the
	// pipeline headings only nest a level deeper, for compound requests.
	if len(children) == 0 {
		for i, s := range sections {
			if i > 0 {
				sb.WriteString("\n---\n\n")
			}
			fmt.Fprintf(&sb, "## Pipeline: `%s`\n\n%s", s.pipeline, s.content)
		}
	} else {
		ref := dryRun.Spec.ResourceRequestRef
		fmt.Fprintf(&sb, "## Compound request: `%s` / `%s`\n\n", promiseName, ref.Name)
		for _, s := range sections {
			fmt.Fprintf(&sb, "### Pipeline: `%s`\n\n%s\n", s.pipeline, s.content)
		}
		for i := range children {
			body, err := r.componentSummaryBody(ctx, &children[i], namespace)
			if err != nil {
				return err
			}
			sb.WriteString("\n---\n\n")
			fmt.Fprintf(&sb, "## Component request: `%s` / `%s`\n\n",
				children[i].Spec.PromiseRef.Name, children[i].Spec.ResourceRequestRef.Name)
			sb.WriteString(body)
		}
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
					Filepath: dryRunSummaryFilepath(dryRun),
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

func (r *DryRunReconciler) markDryRunCompleted(
	ctx context.Context, dryRun *v1alpha1.DryRun, children []v1alpha1.DryRun,
) error {
	apiMeta.SetStatusCondition(&dryRun.Status.Conditions, metav1.Condition{
		Type:               dryRunCompletedCondition,
		Status:             metav1.ConditionTrue,
		Reason:             "SummaryWritten",
		Message:            "Dry run completed; summary written to output repository",
		LastTransitionTime: metav1.Now(),
	})

	// Completed stays True even when a component failed, because a partial summary is
	// still worth reading. ComponentsSucceeded is what tells a consumer whether the
	// preview is COMPLETE -- gating on Completed alone would approve a preview that
	// silently omits a component.
	if len(children) > 0 {
		components := componentStatuses(children)
		dryRun.Status.Components = components
		apiMeta.SetStatusCondition(&dryRun.Status.Conditions, componentsSucceededCondition(components))
	}

	return r.Client.Status().Update(ctx, dryRun)
}

// syncComponentStatus keeps status.components current while the preview runs, writing
// only when something actually changed so a pending compound run does not update its
// status every requeue.
func (r *DryRunReconciler) syncComponentStatus(
	ctx context.Context, dryRun *v1alpha1.DryRun, children []v1alpha1.DryRun,
) error {
	if len(children) == 0 {
		return nil
	}
	components := componentStatuses(children)
	if reflect.DeepEqual(dryRun.Status.Components, components) {
		return nil
	}
	dryRun.Status.Components = components
	return r.Client.Status().Update(ctx, dryRun)
}

func componentStatuses(children []v1alpha1.DryRun) []v1alpha1.DryRunComponentStatus {
	components := make([]v1alpha1.DryRunComponentStatus, 0, len(children))
	for i := range children {
		child := &children[i]
		component := v1alpha1.DryRunComponentStatus{
			Promise:   child.Spec.PromiseRef.Name,
			Request:   child.Spec.ResourceRequestRef.Name,
			Namespace: child.Spec.ResourceRequestRef.Namespace,
			DryRun:    child.Name,
			Phase:     v1alpha1.DryRunComponentPending,
		}
		for _, c := range child.Status.Conditions {
			if c.Type != dryRunCompletedCondition {
				continue
			}
			if c.Status == metav1.ConditionTrue {
				component.Phase = v1alpha1.DryRunComponentSucceeded
			} else {
				component.Phase = v1alpha1.DryRunComponentFailed
				component.Message = c.Message
			}
			break
		}
		components = append(components, component)
	}
	return components
}

func componentsSucceededCondition(components []v1alpha1.DryRunComponentStatus) metav1.Condition {
	var failed, incomplete []string
	for _, c := range components {
		switch c.Phase {
		case v1alpha1.DryRunComponentFailed:
			failed = append(failed, c.Promise+"/"+c.Request)
		case v1alpha1.DryRunComponentPending:
			incomplete = append(incomplete, c.Promise+"/"+c.Request)
		}
	}

	switch {
	case len(failed) > 0:
		return metav1.Condition{
			Type:               dryRunComponentsSucceededCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "ComponentDryRunFailed",
			Message:            "Preview is incomplete; these components failed: " + strings.Join(failed, ", "),
			LastTransitionTime: metav1.Now(),
		}
	case len(incomplete) > 0:
		return metav1.Condition{
			Type:   dryRunComponentsSucceededCondition,
			Status: metav1.ConditionFalse,
			Reason: "ComponentDryRunIncomplete",
			Message: "Preview is incomplete; these components did not finish within " +
				componentDryRunTimeout.String() + ": " + strings.Join(incomplete, ", "),
			LastTransitionTime: metav1.Now(),
		}
	default:
		return metav1.Condition{
			Type:               dryRunComponentsSucceededCondition,
			Status:             metav1.ConditionTrue,
			Reason:             "AllComponentsSucceeded",
			Message:            "All component dry runs completed",
			LastTransitionTime: metav1.Now(),
		}
	}
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
		// Component DryRuns are owned by the compound one, so completing a component
		// wakes the compound reconcile instead of waiting out the requeue interval.
		Owns(&v1alpha1.DryRun{}).
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
		diff := fmt.Sprintf("--- a/%s\n+++ b/%s\n%s", path, path, dryRunLineDiff(prev[path], next[path]))
		fmt.Fprintf(&b, "\n---\n\n## ✏️ Modified: `%s`\n\n```diff\n%s```\n", path, diff)
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
			fmt.Fprintf(&sb, "%s%s\n", prefix, line)
		}
	}
	return sb.String()
}
