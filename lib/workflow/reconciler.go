package workflow

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/resourceutil"
	"gopkg.in/yaml.v2"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WorkflowParams struct {
	ctx                context.Context
	client             client.Client
	logger             logr.Logger
	parentObject       *unstructured.Unstructured
	pipelineResources  []v1alpha1.PipelineJobResources
	workflowType       string
	numberOfJobsToKeep int
	eventRecorder      record.EventRecorder
	namespace          string
	skipConditions     bool
}

func (o *WorkflowParams) SetSkipConditions(skip bool) {
	o.skipConditions = skip
}

func (o *WorkflowParams) SkipConditions() bool {
	return o.skipConditions
}

func (o *WorkflowParams) PipelineResources() []v1alpha1.PipelineJobResources {
	return o.pipelineResources
}

var (
	minimumPeriodBetweenCreatingPipelineResources = 1100 * time.Millisecond
	//TODO is this the right way to do this? Can't remember how its changed in latest go versions
	ErrDeletePipelineFailed = fmt.Errorf("Delete Pipeline Failed")
)

func NewWorkflowParams(ctx context.Context, client client.Client, eventRecorder record.EventRecorder, logger logr.Logger, parentObj *unstructured.Unstructured, resources []v1alpha1.PipelineJobResources, workflowType string, numberOfJobsToKeep int, namespace string) WorkflowParams {
	return WorkflowParams{
		ctx:                ctx,
		client:             client,
		logger:             logger,
		parentObject:       parentObj,
		workflowType:       workflowType,
		numberOfJobsToKeep: numberOfJobsToKeep,
		pipelineResources:  resources,
		eventRecorder:      eventRecorder,
		namespace:          namespace,
	}
}

// ReconcileDelete deletes Workflows.
func ReconcileDelete(wp WorkflowParams) (bool, error) {
	logging.Debug(wp.logger, "reconciling delete pipeline")

	if len(wp.pipelineResources) == 0 {
		return false, nil
	}

	if len(wp.pipelineResources) > 1 {
		logging.Warn(wp.logger, "multiple delete pipelines found; only the first will be used")
	}

	pipeline := wp.pipelineResources[0]
	isManualReconciliation := isManualReconciliation(wp.parentObject.GetLabels())
	mostRecentJob, err := getMostRecentDeletePipelineJob(wp, wp.namespace, pipeline)
	if err != nil {
		return false, err
	}

	if isManualReconciliation {
		logging.Info(wp.logger, "manual reconciliation detected for delete pipeline", "pipeline", pipeline.Name)
	}

	if jobIsInflight(mostRecentJob) {
		if isManualReconciliation {
			logging.Info(wp.logger, "suspending job for manual reconciliation", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
			if err = suspendJob(wp.ctx, wp.client, mostRecentJob); err != nil {
				logging.Error(wp.logger, err, "failed to suspend job", "job", mostRecentJob.GetName())
			}
			wp.eventRecorder.Eventf(wp.parentObject, "Normal", "PipelineSuspended", "Delete Pipeline suspended: %s", wp.pipelineResources[0].Name)
			return true, err
		}

		logging.Debug(wp.logger, "job already inflight for pipeline; waiting for completion", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
		return true, nil
	}

	if mostRecentJob == nil || isManualReconciliation {
		return createDeletePipeline(wp, pipeline)
	}

	logging.Debug(wp.logger, "checking status of delete pipeline")
	if mostRecentJob.Status.Succeeded > 0 {
		logging.Info(wp.logger, "delete pipeline completed")
		return false, nil
	}
	if mostRecentJob.Status.Failed > 0 {
		return false, ErrDeletePipelineFailed
	}

	logging.Debug(wp.logger, "delete pipeline still running", "status", mostRecentJob.Status)
	return true, nil
}

func createDeletePipeline(wp WorkflowParams, pipeline v1alpha1.PipelineJobResources) (abort bool, err error) {
	logging.Debug(wp.logger, "creating delete pipeline; execution will commence")
	if isManualReconciliation(wp.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(wp); err != nil {
			return false, err
		}
	}
	//TODO retrieve error information from applyResources to return to the caller
	applyResources(wp, append(pipeline.GetObjects(), pipeline.Job)...)
	wp.eventRecorder.Eventf(wp.parentObject, "Normal", "PipelineStarted", "Delete Pipeline started: %s", wp.pipelineResources[0].Name)
	return true, nil
}

func ReconcileConfigure(wp WorkflowParams) (requeue bool, err error) {
	mostRecentJob, pipelineIndex, err := determinePipelineIndex(wp)
	if err != nil {
		return false, err
	}
	logging.Trace(wp.logger, "calculated pipeline index", "index", pipelineIndex)

	if err = initialiseWorkflowStatusCounts(wp, pipelineIndex); err != nil {
		logging.Error(wp.logger, err, "failed to initialise workflow counts")
		return false, err
	}

	if pipelineIndex >= len(wp.pipelineResources) {
		//Lets see if this breaks stuff
		logging.Info(wp.logger, "all pipelines complete; nothing more to reconcile")
		return false, cleanup(wp, wp.namespace)
		// pipelineIndex = len(wp.pipelineResources) - 1
	}

	if pipelineIndex < 0 {
		logging.Debug(wp.logger, "no pipeline to reconcile")
		return false, nil
	}

	logging.Info(wp.logger, "reconciling configure workflow", "pipelineIndex", pipelineIndex, "mostRecentJobName", mostRecentJobName(mostRecentJob))

	pipeline := wp.pipelineResources[pipelineIndex]
	isManualReconciliation := isManualReconciliation(wp.parentObject.GetLabels())
	wp.logger = wp.logger.WithName(pipeline.Name).WithValues("isManualReconciliation", isManualReconciliation)

	//TODO waiting for derik to explain this to me on slack
	// logging.Trace(wp.logger, "checking if job is for pipeline", "job", mostRecentJob.Name, "pipeline", pipeline.Name)

	if pipelineIsNotDeployed(pipeline, mostRecentJob) {
		if jobIsNotInflight(mostRecentJob) {
			return createConfigurePipeline(wp, pipelineIndex, pipeline)
		}

		logging.Info(wp.logger, "job already inflight for another workflow; suspending it", "job", mostRecentJob.Name)
		err = suspendJob(wp.ctx, wp.client, mostRecentJob)
		if err != nil {
			logging.Error(wp.logger, err, "failed to suspend job", "job", mostRecentJob.GetName())
		}
		return true, nil
	}

	logging.Debug(wp.logger, "job already deployed", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
	if jobIsInflight(mostRecentJob) && !isManualReconciliation {
		logging.Debug(wp.logger, "job already inflight for pipeline; waiting for completion", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
		return true, nil
	}

	if isManualReconciliation {
		if jobIsInflight(mostRecentJob) {
			logging.Info(wp.logger, "suspending job for manual reconciliation", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
			if err = suspendJob(wp.ctx, wp.client, mostRecentJob); err != nil {
				logging.Error(wp.logger, err, "failed to suspend job", "job", mostRecentJob.GetName())
			}
			return true, err
		}
		logging.Info(wp.logger, "pipeline running due to manual reconciliation", "pipeline", pipeline.Name, "parentLabels", wp.parentObject.GetLabels())
		return createConfigurePipeline(wp, pipelineIndex, pipeline)
	}

	if isFailed(mostRecentJob) {
		return setFailedConditionAndEvents(wp, mostRecentJob, pipeline)
	}

	logging.Debug(wp.logger, "pipeline reconciliation complete; running cleanup")
	return false, cleanup(wp, wp.namespace)
}

func mostRecentJobName(mostRecentJob *batchv1.Job) string {
	if mostRecentJob == nil {
		return "n/a"
	}
	return mostRecentJob.Name
}

func determinePipelineIndex(wp WorkflowParams) (*batchv1.Job, int, error) {
	allJobs, err := getAllAssociatedJobs(wp)
	if err != nil {
		return nil, -1, err
	}

	var mostRecentJob *batchv1.Job
	pipelineIndex := 0

	if len(allJobs) != 0 {
		logging.Debug(wp.logger, "found existing jobs; checking pipeline match for most recent job")
		allJobs = resourceutil.SortJobsByCreationDateTime(allJobs, false)
		mostRecentJob = &allJobs[0]
		pipelineIndex = nextPipelineIndex(wp, mostRecentJob)
	}
	return mostRecentJob, pipelineIndex, nil
}

func initialiseWorkflowStatusCounts(wp WorkflowParams, pipelineIndex int) error {
	if wp.skipConditions {
		return nil
	}

	updateStatus := false
	if pipelineIndex == 0 {
		workflowsFailed := resourceutil.GetWorkflowsCounterStatus(wp.parentObject, "workflowsFailed")
		if workflowsFailed != 0 {
			resourceutil.SetStatus(wp.parentObject, wp.logger, "workflowsFailed", int64(0))
			updateStatus = true
		}
	}

	workflowsSucceededCount := resourceutil.GetWorkflowsCounterStatus(wp.parentObject, "workflowsSucceeded")
	if updateStatus || workflowsSucceededCount != int64(pipelineIndex) {
		resourceutil.SetStatus(wp.parentObject, wp.logger, "workflowsSucceeded", int64(pipelineIndex))
		if err := wp.client.Status().Update(wp.ctx, wp.parentObject); err != nil {
			logging.Error(wp.logger, err, "failed to update parent object status")
			return err
		}
	}
	return nil
}

func getAllAssociatedJobs(wp WorkflowParams) ([]batchv1.Job, error) {
	allJobs, err := getJobsWithLabels(wp, labelsForJobs(wp), wp.namespace)
	if err != nil {
		logging.Error(wp.logger, err, "failed to list jobs")
		return nil, err
	}

	// TODO: this part will be deprecated when we stop using the legacy labels
	allLegacyJobs, err := getJobsWithLabels(wp, legacyLabelsForJobs(wp), wp.namespace)
	if err != nil {
		logging.Error(wp.logger, err, "failed to list jobs")
		return nil, err
	}
	allJobs = append(allJobs, allLegacyJobs...)
	return allJobs, nil
}

func setFailedConditionAndEvents(wp WorkflowParams, mostRecentJob *batchv1.Job, pipeline v1alpha1.PipelineJobResources) (bool, error) {
	if !wp.skipConditions {
		resourceutil.MarkConfigureWorkflowAsFailed(wp.logger, wp.parentObject, pipeline.Name)
		resourceutil.MarkReconciledFailing(wp.parentObject, resourceutil.ConfigureWorkflowCompletedFailedReason)
		resourceutil.SetStatus(wp.parentObject, wp.logger, "workflowsFailed", int64(1))
		if err := wp.client.Status().Update(wp.ctx, wp.parentObject); err != nil {
			logging.Error(wp.logger, err, "failed to update parent object status")
			return false, err
		}
	}
	wp.eventRecorder.Eventf(wp.parentObject, v1.EventTypeWarning,
		resourceutil.ConfigureWorkflowCompletedFailedReason, "A %s/configure Pipeline has failed: %s", wp.workflowType, pipeline.Name)
	logging.Warn(wp.logger, "pipeline job failed; exiting workflow", "failedJob", mostRecentJob.Name, "pipeline", pipeline.Name)
	return true, nil
}

func suspendJob(ctx context.Context, c client.Client, job *batchv1.Job) error {
	trueBool := true
	patch := client.MergeFrom(job.DeepCopy())
	job.Spec.Suspend = &trueBool
	return c.Patch(ctx, job, patch)
}

func getLabelsForPipelineJob(pipeline v1alpha1.PipelineJobResources) map[string]string {
	labels := pipeline.Job.DeepCopy().GetLabels()
	return labels
}

func labelsForJobs(wp WorkflowParams) map[string]string {
	l := map[string]string{
		v1alpha1.WorkflowTypeLabel: wp.workflowType,
	}
	promiseName := wp.parentObject.GetName()
	if strings.HasPrefix(wp.workflowType, string(v1alpha1.WorkflowTypeResource)) {
		promiseName = wp.parentObject.GetLabels()[v1alpha1.PromiseNameLabel]
		l[v1alpha1.ResourceNameLabel] = wp.parentObject.GetName()
		if wp.namespace != wp.parentObject.GetNamespace() {
			// only set resource request namespace label when workflow running in different namespace from the resource requests
			l[v1alpha1.ResourceNamespaceLabel] = wp.parentObject.GetNamespace()
		}
	}
	l[v1alpha1.PromiseNameLabel] = promiseName
	return l
}

// TODO: this part will be deprecated when we stop using the legacy labels
func legacyLabelsForJobs(wp WorkflowParams) map[string]string {
	l := map[string]string{
		v1alpha1.WorkTypeLabel: wp.workflowType,
	}
	promiseName := wp.parentObject.GetName()
	if wp.workflowType == string(v1alpha1.WorkTypeResource) {
		promiseName = wp.parentObject.GetLabels()[v1alpha1.PromiseNameLabel]
		l[v1alpha1.ResourceNameLabel] = wp.parentObject.GetName()
	}
	l[v1alpha1.PromiseNameLabel] = promiseName
	return l
}

func labelsForAllPipelineJobs(pipeline v1alpha1.PipelineJobResources) map[string]string {
	pipelineLabels := pipeline.Job.GetLabels()
	labels := map[string]string{
		v1alpha1.PromiseNameLabel: pipelineLabels[v1alpha1.PromiseNameLabel],
	}
	if pipelineLabels[v1alpha1.ResourceNameLabel] != "" {
		labels[v1alpha1.ResourceNameLabel] = pipelineLabels[v1alpha1.ResourceNameLabel]
	}
	if pipelineLabels[v1alpha1.ResourceNamespaceLabel] != "" {
		labels[v1alpha1.ResourceNamespaceLabel] = pipelineLabels[v1alpha1.ResourceNamespaceLabel]
	}
	return labels
}

func pipelineIsNotDeployed(pipeline v1alpha1.PipelineJobResources, job *batchv1.Job) bool {
	return !pipelineIsAlreadyDeployed(pipeline, job)
}

func pipelineIsAlreadyDeployed(pipeline v1alpha1.PipelineJobResources, job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	jobLabels := job.GetLabels()
	pipelineLabels := pipeline.Job.GetLabels()

	if jobLabels[v1alpha1.KratixResourceHashLabel] != pipelineLabels[v1alpha1.KratixResourceHashLabel] {
		return false
	}

	if jobLabels[v1alpha1.WorkflowTypeLabel] != pipelineLabels[v1alpha1.WorkflowTypeLabel] {
		return false
	}

	if jobLabels[v1alpha1.WorkflowActionLabel] != pipelineLabels[v1alpha1.WorkflowActionLabel] {
		return false
	}

	if jobLabels[v1alpha1.KratixPipelineHashLabel] != pipelineLabels[v1alpha1.KratixPipelineHashLabel] {
		return false
	}

	return jobLabels[v1alpha1.PipelineNameLabel] == pipelineLabels[v1alpha1.PipelineNameLabel]
}

func nextPipelineIndex(wp WorkflowParams, mostRecentJob *batchv1.Job) int {
	if mostRecentJob == nil || isManualReconciliation(wp.parentObject.GetLabels()) {
		return 0
	}

	// in reverse order loop through the pipeline, see if the latest job is for
	// the pipeline if it is and its finished then we know the pipeline at the
	// index is done, and we need to start the next one
	i := len(wp.pipelineResources) - 1
	for i >= 0 {
		if pipelineIsAlreadyDeployed(wp.pipelineResources[i], mostRecentJob) {
			logging.Trace(wp.logger, "found job for pipeline", "pipeline", wp.pipelineResources[i].Name, "job", mostRecentJob.Name, "status", mostRecentJob.Status, "index", i)
			if isFailed(mostRecentJob) || jobIsInflight(mostRecentJob) {
				return i
			}
			break
		}
		i -= 1
	}

	return i + 1
}

func isFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed || condition.Type == batchv1.JobSuspended {
			return true
		}
	}
	return false
}

func jobIsNotInflight(job *batchv1.Job) bool {
	return !jobIsInflight(job)
}

func jobIsInflight(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	if job.Status.Active > 0 {
		return true
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobSuspended || condition.Type == batchv1.JobFailed {
			return false
		}
	}
	return true
}

func cleanup(wp WorkflowParams, namespace string) error {
	pipelineNames := map[string]bool{}
	for _, pipeline := range wp.pipelineResources {
		l := labelsForAllPipelineJobs(pipeline)
		l[v1alpha1.PipelineNameLabel] = pipeline.Name
		pipelineNames[pipeline.Name] = true
		jobsForPipeline, _ := getJobsWithLabels(wp, l, namespace)
		if err := cleanupJobs(wp, jobsForPipeline); err != nil {
			logging.Error(wp.logger, err, "failed to delete old jobs")
			return err
		}
	}

	allPipelineWorks, err := resourceutil.GetWorksByType(wp.client, v1alpha1.Type(wp.workflowType), wp.parentObject)
	if err != nil {
		logging.Error(wp.logger, err, "failed to list works for Promise", "promise", wp.parentObject.GetName())
		return err
	}
	for _, work := range allPipelineWorks {
		workPipelineName := work.GetLabels()[v1alpha1.PipelineNameLabel]
		if !pipelineNames[workPipelineName] {
			logging.Debug(wp.logger, "deleting old work", "work", work.GetName(), "objectName", wp.parentObject.GetName(), "workType", work.Labels[v1alpha1.WorkTypeLabel])
			if err := wp.client.Delete(wp.ctx, &work); err != nil {
				logging.Error(wp.logger, err, "failed to delete old work", "work", work.GetName())
				return err
			}

		}
	}

	return nil
}

func cleanupJobs(wp WorkflowParams, pipelineJobsAtCurrentSpec []batchv1.Job) error {
	if len(pipelineJobsAtCurrentSpec) <= wp.numberOfJobsToKeep {
		return nil
	}

	// Sort jobs by creation time
	pipelineJobsAtCurrentSpec = resourceutil.SortJobsByCreationDateTime(pipelineJobsAtCurrentSpec, true)

	// Delete all but the last n jobs; n defaults to 5 and can be configured by env var for the operator
	for i := 0; i < len(pipelineJobsAtCurrentSpec)-wp.numberOfJobsToKeep; i++ {
		job := pipelineJobsAtCurrentSpec[i]
		logging.Debug(wp.logger, "deleting old job", "job", job.GetName())
		if err := wp.client.Delete(wp.ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if !errors.IsNotFound(err) {
				logging.Warn(wp.logger, "failed to delete job; will retry", "job", job.GetName(), "error", err)
				return nil
			}
		}
	}

	return nil
}

func createConfigurePipeline(wp WorkflowParams, pipelineIndex int, resources v1alpha1.PipelineJobResources) (abort bool, err error) {
	updated, err := setConfigureWorkflowCompletedConditionStatus(wp, pipelineIndex == 0, wp.parentObject)
	if err != nil || updated {
		return updated, err
	}

	logging.Info(wp.logger, "triggering pipeline", "workflowAction", resources.WorkflowAction)

	var objectToDelete []client.Object
	if objectToDelete, err = getObjectsToDelete(wp, resources); err != nil {
		return false, err
	}

	logging.Trace(wp.logger, "reconciling for parent object", "parent", wp.parentObject.GetName())
	if isManualReconciliation(wp.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(wp); err != nil {
			return false, err
		}
	}

	deleteResources(wp, objectToDelete...)
	applyResources(wp, append(resources.GetObjects(), resources.Job)...)

	wp.eventRecorder.Eventf(wp.parentObject, "Normal", "PipelineStarted", "Configure Pipeline started: %s", resources.Name)

	return true, nil
}

func removeManualReconciliationLabel(wp WorkflowParams) error {
	logging.Debug(wp.logger, "manual reconciliation label detected; removing it")
	return removeLabel(wp, resourceutil.ManualReconciliationLabel)
}

func removeLabel(wp WorkflowParams, labelKey string) error {
	newLabels := wp.parentObject.GetLabels()
	delete(newLabels, labelKey)
	wp.parentObject.SetLabels(newLabels)
	if err := wp.client.Update(wp.ctx, wp.parentObject); err != nil {
		logging.Error(wp.logger, err, "failed to remove manual reconciliation label")
		return err
	}
	return nil
}

func setConfigureWorkflowCompletedConditionStatus(wp WorkflowParams, isTheFirstPipeline bool, obj *unstructured.Unstructured) (bool, error) {
	if wp.skipConditions {
		return false, nil
	}
	switch resourceutil.GetConfigureWorkflowCompletedConditionStatus(obj) {
	case v1.ConditionTrue:
		fallthrough
	case v1.ConditionUnknown:
		currentMessage := resourceutil.GetStatus(obj, "message")
		if isTheFirstPipeline || currentMessage == "" || currentMessage == "Resource requested" {
			resourceutil.SetStatus(obj, wp.logger, "message", "Pending")
		}
		resourceutil.MarkConfigureWorkflowAsRunning(wp.logger, obj)
		resourceutil.MarkReconciledPending(obj, "WorkflowPending")
		err := wp.client.Status().Update(wp.ctx, obj)
		if err != nil {
			logging.Error(wp.logger, err, "failed to update object status")
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

func getMostRecentDeletePipelineJob(wp WorkflowParams, namespace string, pipeline v1alpha1.PipelineJobResources) (*batchv1.Job, error) {
	labels := getLabelsForPipelineJob(pipeline)
	jobs, err := getJobsWithLabels(wp, labels, namespace)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	jobs = resourceutil.SortJobsByCreationDateTime(jobs, false)
	return &jobs[0], nil
}

func getJobsWithLabels(wp WorkflowParams, jobLabels map[string]string, namespace string) ([]batchv1.Job, error) {
	selectorLabels := labels.FormatLabels(jobLabels)
	selector, err := labels.Parse(selectorLabels)

	if err != nil {
		return nil, fmt.Errorf("error parsing labels %v: %w", jobLabels, err)
	}

	listOps := &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	}

	jobs := &batchv1.JobList{}
	err = wp.client.List(wp.ctx, jobs, listOps)
	if err != nil {
		logging.Error(wp.logger, err, "error listing jobs", "selectors", selector.String())
		return nil, err
	}
	return jobs.Items, nil
}

func isManualReconciliation(labels map[string]string) bool {
	return isLabelSetToTrue(labels, resourceutil.ManualReconciliationLabel)
}

func isLabelSetToTrue(labels map[string]string, labelKey string) bool {
	if labels == nil {
		return false
	}
	val, exists := labels[labelKey]
	return exists && val == "true"
}

// TODO return error info (summary of errors from resources?) to the caller, instead of just logging
func applyResources(wp WorkflowParams, resources ...client.Object) {
	logging.Debug(wp.logger, "reconciling pipeline resources")

	for _, resource := range resources {
		logger := wp.logger.WithValues("type", reflect.TypeOf(resource), "gvk", resource.GetObjectKind().GroupVersionKind().String(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())

		logging.Debug(logger, "reconciling resource")
		if err := wp.client.Create(wp.ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				if resource.GetObjectKind().GroupVersionKind().Kind == rbacv1.ServiceAccountKind {
					serviceAccount := &v1.ServiceAccount{}
					if err := wp.client.Get(wp.ctx, client.ObjectKey{Namespace: resource.GetNamespace(), Name: resource.GetName()}, serviceAccount); err != nil {
						logging.Error(logger, err, "error getting service account")
						continue
					}

					if _, ok := serviceAccount.Labels[v1alpha1.PromiseNameLabel]; !ok {
						logging.Debug(wp.logger, "service account exists but was not created by kratix; skipping update", "name", serviceAccount.GetName(), "namespace", serviceAccount.GetNamespace(), "labels", serviceAccount.GetLabels())
						continue
					}

				}
				logging.Debug(logger, "resource already exists; updating")
				if err = wp.client.Update(wp.ctx, resource); err == nil {
					continue
				}
			}

			logging.Error(logger, err, "error reconciling resource")
			y, _ := yaml.Marshal(&resource)
			logging.Error(logger, err, string(y))
		} else {
			logging.Debug(logger, "resource created")
		}
	}

	time.Sleep(minimumPeriodBetweenCreatingPipelineResources)
}

func deleteResources(wp WorkflowParams, resources ...client.Object) {
	for _, resource := range resources {
		logger := wp.logger.WithValues("type", reflect.TypeOf(resource), "gvk", resource.GetObjectKind().GroupVersionKind().String(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())
		logging.Debug(logger, "deleting resource")
		if err := wp.client.Delete(wp.ctx, resource); err != nil {
			if errors.IsNotFound(err) {
				logging.Debug(logger, "resource already deleted")
				continue
			}
			logging.Error(logger, err, "error deleting resource")
			y, _ := yaml.Marshal(&resource)
			logging.Error(logger, err, string(y))
		} else {
			logging.Debug(logger, "resource deleted")
		}
	}
}
