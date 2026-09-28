package workflow

import (
	"context"
	stderrors "errors"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RemoveUnversionedPipelineRBAC deletes resource pipeline RBAC without a promise version label.
// Pipelines of a versioned promise never use it, but it still grants its permissions to the
// ServiceAccount it binds.
//
// pipelines must be generated without a promise version, so they carry the unversioned names.
// Only objects without a promise version label are deleted: the not-set version uses the same
// names but labels its objects. Nothing is deleted while a pipeline Job without a promise
// version label is still running with it.
//
// TODO: remove soon, once users have upgraded to per-version pipeline RBAC.
func RemoveUnversionedPipelineRBAC(ctx context.Context, c client.Client, logger logr.Logger, pipelines []v1alpha1.PipelineJobResources) error {
	var errs []error
	for _, pipeline := range pipelines {
		running, err := unversionedPipelineIsRunning(ctx, c, pipeline)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if running {
			logging.Debug(logger, "pipeline job without a promise version label is still running; keeping unversioned RBAC", "pipeline", pipeline.Name)
			continue
		}

		for _, obj := range unversionedObjectsByName(pipeline) {
			errs = append(errs, deleteIfUnversioned(ctx, c, logger, obj))
		}

		stale, err := unversionedUserPermissionObjects(ctx, c, pipeline)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, obj := range stale {
			errs = append(errs, deleteIfUnversioned(ctx, c, logger, obj))
		}
	}
	return stderrors.Join(errs...)
}

func unversionedPipelineIsRunning(ctx context.Context, c client.Client, pipeline v1alpha1.PipelineJobResources) (bool, error) {
	jobLabels := pipeline.Job.GetLabels()
	jobs := &batchv1.JobList{}
	if err := c.List(ctx, jobs, client.InNamespace(pipeline.Job.GetNamespace()), client.MatchingLabels{
		v1alpha1.PromiseNameLabel:    jobLabels[v1alpha1.PromiseNameLabel],
		v1alpha1.PipelineNameLabel:   jobLabels[v1alpha1.PipelineNameLabel],
		v1alpha1.WorkflowTypeLabel:   jobLabels[v1alpha1.WorkflowTypeLabel],
		v1alpha1.WorkflowActionLabel: jobLabels[v1alpha1.WorkflowActionLabel],
	}); err != nil {
		return false, err
	}
	for i := range jobs.Items {
		if _, versioned := jobs.Items[i].GetLabels()[v1alpha1.PromiseVersionLabel]; !versioned && isRunning(&jobs.Items[i]) {
			return true, nil
		}
	}
	return false, nil
}

func unversionedObjectsByName(pipeline v1alpha1.PipelineJobResources) []client.Object {
	var objects []client.Object
	// A ServiceAccount named by the user is theirs; only the one named after the pipeline is Kratix's.
	if sa := pipeline.Shared.ServiceAccount; sa != nil && sa.GetName() == pipeline.PipelineID {
		objects = append(objects, sa)
	}
	for i := range pipeline.Shared.Roles {
		objects = append(objects, &pipeline.Shared.Roles[i])
	}
	for i := range pipeline.Shared.RoleBindings {
		objects = append(objects, &pipeline.Shared.RoleBindings[i])
	}
	for i := range pipeline.Shared.ClusterRoles {
		objects = append(objects, &pipeline.Shared.ClusterRoles[i])
	}
	for i := range pipeline.Shared.ClusterRoleBindings {
		objects = append(objects, &pipeline.Shared.ClusterRoleBindings[i])
	}
	return objects
}

// unversionedUserPermissionObjects finds unversioned user-permission RBAC that the current spec
// does not produce, e.g. for a namespace the spec does not list, which lookups by name miss.
func unversionedUserPermissionObjects(ctx context.Context, c client.Client, pipeline v1alpha1.PipelineJobResources) ([]client.Object, error) {
	noVersion, err := labels.NewRequirement(v1alpha1.PromiseVersionLabel, selection.DoesNotExist, nil)
	if err != nil {
		return nil, err
	}
	selector := labels.SelectorFromSet(getPipelineResourcesLabels(pipeline)).Add(*noVersion)
	listOptions := &client.ListOptions{LabelSelector: selector}

	var objects []client.Object
	roles := &rbacv1.RoleList{}
	if err := c.List(ctx, roles, listOptions); err != nil {
		return nil, err
	}
	for i := range roles.Items {
		objects = append(objects, &roles.Items[i])
	}
	roleBindings := &rbacv1.RoleBindingList{}
	if err := c.List(ctx, roleBindings, listOptions); err != nil {
		return nil, err
	}
	for i := range roleBindings.Items {
		objects = append(objects, &roleBindings.Items[i])
	}
	clusterRoles := &rbacv1.ClusterRoleList{}
	if err := c.List(ctx, clusterRoles, listOptions); err != nil {
		return nil, err
	}
	for i := range clusterRoles.Items {
		objects = append(objects, &clusterRoles.Items[i])
	}
	clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
	if err := c.List(ctx, clusterRoleBindings, listOptions); err != nil {
		return nil, err
	}
	for i := range clusterRoleBindings.Items {
		objects = append(objects, &clusterRoleBindings.Items[i])
	}
	return objects, nil
}

func deleteIfUnversioned(ctx context.Context, c client.Client, logger logr.Logger, obj client.Object) error {
	existing, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return nil
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	existingLabels := existing.GetLabels()
	if _, versioned := existingLabels[v1alpha1.PromiseVersionLabel]; versioned {
		return nil
	}
	if existingLabels[v1alpha1.PromiseNameLabel] != obj.GetLabels()[v1alpha1.PromiseNameLabel] {
		return nil
	}

	if err := c.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
		return err
	}
	logging.Info(logger, "deleted resource pipeline RBAC without a promise version label", "kind", existing.GetObjectKind().GroupVersionKind().Kind, "name", existing.GetName(), "namespace", existing.GetNamespace())
	return nil
}
