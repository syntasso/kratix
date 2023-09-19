package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/writers"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"
)

type StateStore interface {
	client.Object
	GetSecretRef() *v1.SecretReference
}

type commonArgs struct {
	ctx    context.Context
	client client.Client
	logger logr.Logger
}

type jobArg struct {
	commonArgs
	obj               *unstructured.Unstructured
	pipelineLabels    map[string]string
	pipelineResources []client.Object
}

func ensurePipelineIsReconciled(j jobArg) (*ctrl.Result, error) {
	namespace := j.obj.GetNamespace()
	if namespace == "" {
		namespace = kratixPlatformSystemNamespace
	}

	pipelineJobs, err := getJobsWithLabels(j.commonArgs, j.pipelineLabels, namespace)
	if err != nil {
		j.logger.Info("Failed getting Promise pipeline jobs", "error", err)
		return &slowRequeue, nil
	}

	// No jobs indicates this is the first reconciliation loop of this resource request
	if len(pipelineJobs) == 0 {
		j.logger.Info("No jobs found, creating workflow Job")
		return &fastRequeue, createConfigurePipeline(j)
	}

	existingPipelineJob, err := resourceutil.PipelineExists(j.logger, j.obj, pipelineJobs)
	if err != nil {
		return &slowRequeue, nil
	}

	if resourceutil.IsThereAPipelineRunning(j.logger, pipelineJobs) {
		for _, job := range resourceutil.SuspendablePipelines(j.logger, pipelineJobs) {
			//Don't suspend a the job that is the desired spec
			if existingPipelineJob != nil && job.GetName() != existingPipelineJob.GetName() {
				trueBool := true
				patch := client.MergeFrom(job.DeepCopy())
				job.Spec.Suspend = &trueBool
				j.logger.Info("Suspending inactive job", "job", job.GetName())
				err := j.client.Patch(j.ctx, &job, patch)
				if err != nil {
					j.logger.Error(err, "failed to patch Job", "job", job.GetName())
				}
			}
		}
		j.logger.Info("Job already inflight for workflow, waiting for it to be inactive")
		return &slowRequeue, nil
	}

	if isManualReconciliation(j.obj.GetLabels()) || existingPipelineJob == nil {
		j.logger.Info("Creating job for workflow", "manualTrigger", isManualReconciliation(j.obj.GetLabels()))
		return &fastRequeue, createConfigurePipeline(j)
	}

	j.logger.Info("Job already exists and is complete for workflow")
	return nil, nil
}

func createConfigurePipeline(j jobArg) error {
	updated, err := setPipelineCompletedConditionStatus(j.commonArgs, j.obj)
	if err != nil {
		return err
	}

	if updated {
		return nil
	}

	j.logger.Info("Triggering Promise pipeline")

	applyResources(j.commonArgs, j.pipelineResources...)

	if isManualReconciliation(j.obj.GetLabels()) {
		newLabels := j.obj.GetLabels()
		delete(newLabels, resourceutil.ManualReconciliationLabel)
		j.obj.SetLabels(newLabels)
		if err := j.client.Update(j.ctx, j.obj); err != nil {
			return err
		}
	}

	return nil
}

// pass in nil resourceLabels to delete all resources of the GVK
func deleteAllResourcesWithKindMatchingLabel(args commonArgs, gvk schema.GroupVersionKind, resourceLabels map[string]string) (bool, error) {
	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(gvk)
	listOptions := client.ListOptions{LabelSelector: labels.SelectorFromSet(resourceLabels)}
	err := args.client.List(args.ctx, resourceList, &listOptions)
	if err != nil {
		return true, err
	}

	args.logger.Info("deleting resources", "kind", resourceList.GetKind(), "withLabels", resourceLabels, "resources", getResourceNames(resourceList.Items))

	for _, resource := range resourceList.Items {
		err = args.client.Delete(args.ctx, &resource, client.PropagationPolicy(metav1.DeletePropagationBackground))
		if err != nil && !errors.IsNotFound(err) {
			args.logger.Error(err, "Error deleting resource, will try again in 5 seconds", "name", resource.GetName(), "kind", resource.GetKind())
			return true, err
		}
		args.logger.Info("successfully triggered deletion of resource", "name", resource.GetName(), "kind", resource.GetKind())
	}

	return len(resourceList.Items) != 0, nil
}

func getResourceNames(items []unstructured.Unstructured) []string {
	var names []string
	for _, item := range items {
		resource := item.GetName()
		//if the resource is destination scoped it has no namespace
		if item.GetNamespace() != "" {
			resource = fmt.Sprintf("%s/%s", item.GetNamespace(), item.GetName())
		}
		names = append(names, resource)
	}

	return names
}

// finalizers must be less than 64 characters
func addFinalizers(args commonArgs, resource client.Object, finalizers []string) (ctrl.Result, error) {
	args.logger.Info("Adding missing finalizers",
		"expectedFinalizers", finalizers,
		"existingFinalizers", resource.GetFinalizers(),
	)
	for _, finalizer := range finalizers {
		controllerutil.AddFinalizer(resource, finalizer)
	}
	if err := args.client.Update(args.ctx, resource); err != nil {
		return defaultRequeue, err
	}
	return ctrl.Result{}, nil
}

func finalizersAreMissing(resource client.Object, finalizers []string) bool {
	for _, finalizer := range finalizers {
		if !controllerutil.ContainsFinalizer(resource, finalizer) {
			return true
		}
	}
	return false
}

func doesNotContainFinalizer(resource client.Object, finalizer string) bool {
	return !controllerutil.ContainsFinalizer(resource, finalizer)
}

func finalizersAreDeleted(resource client.Object, finalizers []string) bool {
	for _, finalizer := range finalizers {
		if controllerutil.ContainsFinalizer(resource, finalizer) {
			return false
		}
	}
	return true
}

func fetchObjectAndSecret(args commonArgs, stateStoreRef client.ObjectKey, stateStore StateStore) (*v1.Secret, error) {
	if err := args.client.Get(args.ctx, stateStoreRef, stateStore); err != nil {
		args.logger.Error(err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "stateStoreRef", stateStoreRef)
		return nil, err
	}

	secret := &v1.Secret{}
	secretRef := types.NamespacedName{
		Name:      stateStore.GetSecretRef().Name,
		Namespace: kratixPlatformSystemNamespace,
	}

	if err := args.client.Get(args.ctx, secretRef, secret); err != nil {
		args.logger.Error(err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "secretRef", secretRef)
		return nil, err
	}

	return secret, nil
}

func newWriter(args commonArgs, destination platformv1alpha1.Destination) (writers.StateStoreWriter, error) {
	stateStoreRef := client.ObjectKey{
		Name:      destination.Spec.StateStoreRef.Name,
		Namespace: destination.Namespace,
	}

	var writer writers.StateStoreWriter
	var err error
	switch destination.Spec.StateStoreRef.Kind {
	case "BucketStateStore":
		stateStore := &platformv1alpha1.BucketStateStore{}
		secret, fetchErr := fetchObjectAndSecret(args, stateStoreRef, stateStore)
		if fetchErr != nil {
			return nil, fetchErr
		}

		writer, err = writers.NewS3Writer(args.logger.WithName("writers").WithName("BucketStateStoreWriter"), stateStore.Spec, destination, secret.Data)
	case "GitStateStore":
		stateStore := &platformv1alpha1.GitStateStore{}
		secret, fetchErr := fetchObjectAndSecret(args, stateStoreRef, stateStore)
		if fetchErr != nil {
			return nil, fetchErr
		}

		writer, err = writers.NewGitWriter(args.logger.WithName("writers").WithName("GitStateStoreWriter"), stateStore.Spec, destination, secret.Data)
	default:
		return nil, fmt.Errorf("unsupported kind %s", destination.Spec.StateStoreRef.Kind)
	}

	if err != nil {
		//TODO: should this be a retryable error?
		args.logger.Error(err, "unable to create StateStoreWriter")
		return nil, err
	}
	return writer, nil
}

func getJobsWithLabels(args commonArgs, jobLabels map[string]string, namespace string) ([]batchv1.Job, error) {
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
	err = args.client.List(args.ctx, jobs, listOps)
	if err != nil {
		args.logger.Error(err, "error listing jobs", "selectors", selector.String())
		return nil, err
	}
	return jobs.Items, nil
}

func applyResources(args commonArgs, resources ...client.Object) {
	args.logger.Info("Reconciling pipeline resources")

	for _, resource := range resources {
		logger := args.logger.WithValues("kind", resource.GetObjectKind().GroupVersionKind().Kind, "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())

		logger.Info("Reconciling")
		if err := args.client.Create(args.ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				logger.Info("Resource already exists, will update")
				if err = args.client.Update(args.ctx, resource); err == nil {
					continue
				}
			}

			logger.Error(err, "Error reconciling on resource")
			y, _ := yaml.Marshal(&resource)
			logger.Error(err, string(y))
		} else {
			logger.Info("Resource created")
		}
	}
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
