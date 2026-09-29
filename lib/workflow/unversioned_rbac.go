package workflow

import (
	"context"
	stderrors "errors"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
// names but labels its objects. Nothing is deleted for a pipeline while any of its Jobs without
// a promise version label is still running, in any namespace. Once none is, the pipeline's
// unversioned RBAC is deleted in every namespace, so it does not wait for each namespace's
// requests to reconcile.
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

		objects, err := unversionedObjects(ctx, c, pipeline)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, obj := range objects {
			errs = append(errs, deleteIfUnversioned(ctx, c, logger, obj))
		}
	}
	return stderrors.Join(errs...)
}

// unversionedPipelineIsRunning looks in every namespace, because the pipeline's user-permission
// ClusterRoles are shared by the pipelines of all namespaces.
func unversionedPipelineIsRunning(ctx context.Context, c client.Client, pipeline v1alpha1.PipelineJobResources) (bool, error) {
	jobLabels := pipeline.Job.GetLabels()
	jobs := &batchv1.JobList{}
	if err := c.List(ctx, jobs, client.MatchingLabels{
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

// unversionedObjects finds the pipeline's unversioned RBAC in every namespace. The pipelines
// only describe one namespace, so namespaced objects are matched by name across namespaces,
// and user-permission objects by their pipeline labels, which also finds those the current
// spec does not produce, e.g. for a namespace the spec does not list.
func unversionedObjects(ctx context.Context, c client.Client, pipeline v1alpha1.PipelineJobResources) ([]client.Object, error) {
	noVersion, err := labels.NewRequirement(v1alpha1.PromiseVersionLabel, selection.DoesNotExist, nil)
	if err != nil {
		return nil, err
	}
	promiseName := pipeline.Job.GetLabels()[v1alpha1.PromiseNameLabel]
	byPromise := &client.ListOptions{LabelSelector: labels.SelectorFromSet(map[string]string{
		v1alpha1.PromiseNameLabel: promiseName,
	}).Add(*noVersion)}

	userPermissionLabels := getPipelineResourcesLabels(pipeline)
	delete(userPermissionLabels, v1alpha1.PipelineNamespaceLabel)
	byUserPermission := &client.ListOptions{LabelSelector: labels.SelectorFromSet(userPermissionLabels).Add(*noVersion)}

	var objects []client.Object
	for i := range pipeline.Shared.ClusterRoles {
		objects = append(objects, &pipeline.Shared.ClusterRoles[i])
	}
	for i := range pipeline.Shared.ClusterRoleBindings {
		objects = append(objects, &pipeline.Shared.ClusterRoleBindings[i])
	}

	// A ServiceAccount named by the user is theirs; only the one named after the pipeline is Kratix's.
	if sa := pipeline.Shared.ServiceAccount; sa != nil && sa.GetName() == pipeline.PipelineID {
		serviceAccounts := &corev1.ServiceAccountList{}
		if err := c.List(ctx, serviceAccounts, byPromise); err != nil {
			return nil, err
		}
		for i := range serviceAccounts.Items {
			if serviceAccounts.Items[i].GetName() == sa.GetName() {
				objects = append(objects, &serviceAccounts.Items[i])
			}
		}
	}

	roleNames := map[string]bool{}
	for _, role := range pipeline.Shared.Roles {
		roleNames[role.GetName()] = true
	}
	for _, listOptions := range []*client.ListOptions{byPromise, byUserPermission} {
		roles := &rbacv1.RoleList{}
		if err := c.List(ctx, roles, listOptions); err != nil {
			return nil, err
		}
		for i := range roles.Items {
			if listOptions == byUserPermission || roleNames[roles.Items[i].GetName()] {
				objects = append(objects, &roles.Items[i])
			}
		}
	}

	roleBindingNames := map[string]bool{}
	for _, roleBinding := range pipeline.Shared.RoleBindings {
		roleBindingNames[roleBinding.GetName()] = true
	}
	for _, listOptions := range []*client.ListOptions{byPromise, byUserPermission} {
		roleBindings := &rbacv1.RoleBindingList{}
		if err := c.List(ctx, roleBindings, listOptions); err != nil {
			return nil, err
		}
		for i := range roleBindings.Items {
			if listOptions == byUserPermission || roleBindingNames[roleBindings.Items[i].GetName()] {
				objects = append(objects, &roleBindings.Items[i])
			}
		}
	}

	clusterRoles := &rbacv1.ClusterRoleList{}
	if err := c.List(ctx, clusterRoles, byUserPermission); err != nil {
		return nil, err
	}
	for i := range clusterRoles.Items {
		objects = append(objects, &clusterRoles.Items[i])
	}
	clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
	if err := c.List(ctx, clusterRoleBindings, byUserPermission); err != nil {
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
