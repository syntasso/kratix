package workflow

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getObjectsToCreate(opts Opts, resourcesObjects []client.Object, objectsToSkip []client.Object) []client.Object {
	var objectsToCreate []client.Object
	for _, resource := range resourcesObjects {
		found := false
		for _, skip := range objectsToSkip {
			if resourcesMatchNamespacedNameAndGVK(resource, skip) {
				opts.logger.Info("Skipping resource because it already exists", "name", resource.GetName(), "namespace", resource.GetNamespace(), "gvk", resource.GetObjectKind().GroupVersionKind())
				found = true
				break
			}
		}
		if !found {
			objectsToCreate = append(objectsToCreate, resource)
		}
	}

	return objectsToCreate
}

func getObjectsToDeleteOrSkip(opts Opts, pipeline v1alpha1.PipelineJobResources) ([]client.Object, []client.Object, error) {
	var toDelete []client.Object
	var toSkip []client.Object
	var err error

	labelSelector := labels.SelectorFromSet(getPipelineResourcesLabels(pipeline))
	listOptions := client.ListOptions{LabelSelector: labelSelector}

	var rolesToDelete []client.Object
	var rolesToSkip []client.Object
	if rolesToDelete, rolesToSkip, err = getRolesToDeleteOrSkip(opts, pipeline.Shared.Roles, listOptions); err != nil {
		return nil, nil, err
	}

	toDelete = append(toDelete, rolesToDelete...)
	toSkip = append(toSkip, rolesToSkip...)

	var roleBindingsToDelete []client.Object
	var roleBindingsToSkip []client.Object
	if roleBindingsToDelete, roleBindingsToSkip, err = getRoleBindingsToDeleteOrSkip(opts, pipeline.Shared.RoleBindings, listOptions); err != nil {
		return nil, nil, err
	}

	toDelete = append(toDelete, roleBindingsToDelete...)
	toSkip = append(toSkip, roleBindingsToSkip...)

	var clusterRolesToDelete []client.Object
	var clusterRolesToSkip []client.Object
	if clusterRolesToDelete, clusterRolesToSkip, err = getClusterRolesToDeleteOrSkip(opts, pipeline.Shared.ClusterRoles, listOptions); err != nil {
		return nil, nil, err
	}

	toDelete = append(toDelete, clusterRolesToDelete...)
	toSkip = append(toSkip, clusterRolesToSkip...)

	var clusterRoleBindingsToDelete []client.Object
	var clusterRoleBindingsToSkip []client.Object
	if clusterRoleBindingsToDelete, clusterRoleBindingsToSkip, err = getClusterRoleBindingsToDeleteOrSkip(opts, pipeline.Shared.ClusterRoleBindings, listOptions); err != nil {
		return nil, nil, err
	}

	toDelete = append(toDelete, clusterRoleBindingsToDelete...)
	toSkip = append(toSkip, clusterRoleBindingsToSkip...)

	return toDelete, toSkip, nil
}

func resourcesMatchNamespacedNameAndGVK(a, b client.Object) bool {
	return a.GetName() == b.GetName() &&
		a.GetNamespace() == b.GetNamespace() &&
		a.GetObjectKind().GroupVersionKind() == b.GetObjectKind().GroupVersionKind()
}

func getRolesToDeleteOrSkip(opts Opts, desiredRoles []rbacv1.Role, listOptions client.ListOptions) ([]client.Object, []client.Object, error) {
	rolesToDelete := []client.Object{}
	rolesToSkip := []client.Object{}
	existingRoles := rbacv1.RoleList{}

	err := opts.client.List(opts.ctx, &existingRoles, &listOptions)

	if err == nil {
		for _, existingRole := range existingRoles.Items {
			delete := true
			for _, desiredRole := range desiredRoles {
				if rolesMatch(existingRole, desiredRole) {
					rolesToSkip = append(rolesToSkip, &desiredRole)
					delete = false
					break
				}
			}

			if delete {
				rolesToDelete = append(rolesToDelete, &existingRole)
			}
		}
	} else if !errors.IsNotFound(err) {
		opts.logger.Error(err, "failed to list user provided permission roles")
		return nil, nil, err
	}

	return rolesToDelete, rolesToSkip, nil
}

func rolesMatch(existingRole rbacv1.Role, desiredRole rbacv1.Role) bool {
	if len(existingRole.Rules) != len(desiredRole.Rules) {
		return false
	}

	for i, existingRule := range existingRole.Rules {
		if existingRule.String() != desiredRole.Rules[i].String() {
			return false
		}
	}

	return true
}

func getRoleBindingsToDeleteOrSkip(opts Opts, desiredRoleBindings []rbacv1.RoleBinding, listOptions client.ListOptions) ([]client.Object, []client.Object, error) {
	roleBindingsToDelete := []client.Object{}
	roleBindingsToSkip := []client.Object{}
	existingRoleBindings := rbacv1.RoleBindingList{}

	err := opts.client.List(opts.ctx, &existingRoleBindings, &listOptions)

	if err == nil {
		for _, existingRoleBinding := range existingRoleBindings.Items {
			delete := true
			for _, desiredRoleBinding := range desiredRoleBindings {
				if roleBindingsMatch(existingRoleBinding, desiredRoleBinding) {
					roleBindingsToSkip = append(roleBindingsToSkip, &desiredRoleBinding)
					delete = false
					break
				}
			}

			if delete {
				roleBindingsToDelete = append(roleBindingsToDelete, &existingRoleBinding)
			}
		}
	} else if !errors.IsNotFound(err) {
		opts.logger.Error(err, "failed to list user provided permission role bindings")
		return nil, nil, err
	}

	return roleBindingsToDelete, roleBindingsToSkip, nil
}

func roleBindingsMatch(existingRoleBinding rbacv1.RoleBinding, desiredRoleBinding rbacv1.RoleBinding) bool {
	if len(existingRoleBinding.Subjects) != len(desiredRoleBinding.Subjects) {
		return false
	}

	for i, existingSubject := range existingRoleBinding.Subjects {
		if existingSubject.String() != desiredRoleBinding.Subjects[i].String() {
			return false
		}
	}

	return existingRoleBinding.RoleRef.String() == desiredRoleBinding.RoleRef.String()
}

func getClusterRolesToDeleteOrSkip(opts Opts, desiredClusterRoles []rbacv1.ClusterRole, listOptions client.ListOptions) ([]client.Object, []client.Object, error) {
	clusterRolesToDelete := []client.Object{}
	clusterRolesToSkip := []client.Object{}
	existingClusterRoles := rbacv1.ClusterRoleList{}

	err := opts.client.List(opts.ctx, &existingClusterRoles, &listOptions)

	if err == nil {
		for _, existingClusterRole := range existingClusterRoles.Items {
			delete := true
			for _, desiredClusterRole := range desiredClusterRoles {
				opts.logger.Info("Checking if cluster roles match", "existing", existingClusterRole.Name, "desired", desiredClusterRole.Name)
				if clusterRolesMatch(existingClusterRole, desiredClusterRole) {
					opts.logger.Info("Cluster roles match")
					clusterRolesToSkip = append(clusterRolesToSkip, &desiredClusterRole)
					delete = false
					break
				}
			}

			if delete {
				opts.logger.Info("No matching cluster role found, deleting", "clusterRole", existingClusterRole.Name)
				clusterRolesToDelete = append(clusterRolesToDelete, &existingClusterRole)
			}
		}
	} else if !errors.IsNotFound(err) {
		opts.logger.Error(err, "failed to list user provided permission cluster roles")
		return nil, nil, err
	}

	return clusterRolesToDelete, clusterRolesToSkip, nil
}

func clusterRolesMatch(existingClusterRole rbacv1.ClusterRole, desiredClusterRole rbacv1.ClusterRole) bool {
	if len(existingClusterRole.Rules) != len(desiredClusterRole.Rules) {
		return false
	}

	for i, existingRule := range existingClusterRole.Rules {
		if existingRule.String() != desiredClusterRole.Rules[i].String() {
			return false
		}
	}

	return true
}

func getClusterRoleBindingsToDeleteOrSkip(opts Opts, desiredClusterRoleBindings []rbacv1.ClusterRoleBinding, listOptions client.ListOptions) ([]client.Object, []client.Object, error) {
	clusterRoleBindingsToDelete := []client.Object{}
	clusterRoleBindingsToSkip := []client.Object{}
	existingClusterRoleBindings := rbacv1.ClusterRoleBindingList{}

	err := opts.client.List(opts.ctx, &existingClusterRoleBindings, &listOptions)

	if err == nil {
		for _, existingClusterRoleBinding := range existingClusterRoleBindings.Items {
			delete := true
			for _, desiredClusterRoleBinding := range desiredClusterRoleBindings {
				if clusterRoleBindingsMatch(existingClusterRoleBinding, desiredClusterRoleBinding) {
					clusterRoleBindingsToSkip = append(clusterRoleBindingsToSkip, &desiredClusterRoleBinding)
					delete = false
					break
				}
			}

			if delete {
				clusterRoleBindingsToDelete = append(clusterRoleBindingsToDelete, &existingClusterRoleBinding)
			}
		}
	} else if !errors.IsNotFound(err) {
		opts.logger.Error(err, "failed to list user provided permission cluster role bindings")
		return nil, nil, err
	}

	return clusterRoleBindingsToDelete, clusterRoleBindingsToSkip, nil
}

func clusterRoleBindingsMatch(existingClusterRoleBinding rbacv1.ClusterRoleBinding, desiredClusterRoleBinding rbacv1.ClusterRoleBinding) bool {
	if len(existingClusterRoleBinding.Subjects) != len(desiredClusterRoleBinding.Subjects) {
		return false
	}

	for i, existingSubject := range existingClusterRoleBinding.Subjects {
		if existingSubject.String() != desiredClusterRoleBinding.Subjects[i].String() {
			return false
		}
	}

	return existingClusterRoleBinding.RoleRef.String() == desiredClusterRoleBinding.RoleRef.String()
}

func getPipelineResourcesLabels(pipeline v1alpha1.PipelineJobResources) map[string]string {
	return map[string]string{
		v1alpha1.PipelineNameLabel: pipeline.Name,
		v1alpha1.PromiseNameLabel:  pipeline.Job.GetLabels()[v1alpha1.PromiseNameLabel],
		v1alpha1.WorkTypeLabel:     pipeline.Job.GetLabels()[v1alpha1.WorkTypeLabel],
		v1alpha1.WorkActionLabel:   pipeline.Job.GetLabels()[v1alpha1.WorkActionLabel],
	}
}
