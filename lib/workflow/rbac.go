package workflow

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getObjectsToDelete(opts Opts, pipeline v1alpha1.PipelineJobResources) ([]client.Object, error) {
	var toDelete []client.Object
	var err error

	labelSelector := labels.SelectorFromSet(getPipelineResourcesLabels(pipeline))
	listOptions := client.ListOptions{LabelSelector: labelSelector}

	var rolesToDelete []client.Object
	if rolesToDelete, err = getRolesToDelete(opts, pipeline.Shared.Roles, listOptions); err != nil {
		return nil, err
	}

	toDelete = append(toDelete, rolesToDelete...)

	var clusterRolesToDelete []client.Object
	if clusterRolesToDelete, err = getClusterRolesToDelete(opts, pipeline.Shared.ClusterRoles, listOptions); err != nil {
		return nil, err
	}

	toDelete = append(toDelete, clusterRolesToDelete...)

	var roleBindingsToDelete []client.Object
	if roleBindingsToDelete, err = getRoleBindingsToDelete(opts, pipeline.Shared.RoleBindings, listOptions); err != nil {
		return nil, err
	}

	toDelete = append(toDelete, roleBindingsToDelete...)

	var clusterRoleBindingsToDelete []client.Object
	if clusterRoleBindingsToDelete, err = getClusterRoleBindingsToDelete(opts, pipeline.Shared.ClusterRoleBindings, listOptions); err != nil {
		return nil, err
	}

	toDelete = append(toDelete, clusterRoleBindingsToDelete...)

	return toDelete, nil
}

func getRolesToDelete(opts Opts, desiredRoles []rbacv1.Role, listOptions client.ListOptions) ([]client.Object, error) {
	rolesToDelete := []client.Object{}
	existingRoles := rbacv1.RoleList{}

	err := opts.client.List(opts.ctx, &existingRoles, &listOptions)

	if err == nil {
		for _, existingRole := range existingRoles.Items {
			delete := true
			for _, desiredRole := range desiredRoles {
				if rolesMatch(existingRole, desiredRole) {
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
		return nil, err
	}

	return rolesToDelete, nil
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

func getRoleBindingsToDelete(opts Opts, desiredRoleBindings []rbacv1.RoleBinding, listOptions client.ListOptions) ([]client.Object, error) {
	roleBindingsToDelete := []client.Object{}
	existingRoleBindings := rbacv1.RoleBindingList{}

	err := opts.client.List(opts.ctx, &existingRoleBindings, &listOptions)

	if err == nil {
		for _, existingRoleBinding := range existingRoleBindings.Items {
			delete := true
			for _, desiredRoleBinding := range desiredRoleBindings {
				if roleBindingsMatch(existingRoleBinding, desiredRoleBinding) {
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
		return nil, err
	}

	return roleBindingsToDelete, nil
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

func getClusterRolesToDelete(opts Opts, desiredClusterRoles []rbacv1.ClusterRole, listOptions client.ListOptions) ([]client.Object, error) {
	clusterRolesToDelete := []client.Object{}
	existingClusterRoles := rbacv1.ClusterRoleList{}

	err := opts.client.List(opts.ctx, &existingClusterRoles, &listOptions)

	if err == nil {
		for _, existingClusterRole := range existingClusterRoles.Items {
			delete := true
			for _, desiredClusterRole := range desiredClusterRoles {
				if clusterRolesMatch(existingClusterRole, desiredClusterRole) {
					delete = false
					break
				}
			}

			if delete {
				clusterRolesToDelete = append(clusterRolesToDelete, &existingClusterRole)
			}
		}
	} else if !errors.IsNotFound(err) {
		opts.logger.Error(err, "failed to list user provided permission cluster roles")
		return nil, err
	}

	return clusterRolesToDelete, nil
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

func getClusterRoleBindingsToDelete(opts Opts, desiredClusterRoleBindings []rbacv1.ClusterRoleBinding, listOptions client.ListOptions) ([]client.Object, error) {
	clusterRoleBindingsToDelete := []client.Object{}
	existingClusterRoleBindings := rbacv1.ClusterRoleBindingList{}

	err := opts.client.List(opts.ctx, &existingClusterRoleBindings, &listOptions)

	if err == nil {
		for _, existingClusterRoleBinding := range existingClusterRoleBindings.Items {
			delete := true
			for _, desiredClusterRoleBinding := range desiredClusterRoleBindings {
				if clusterRoleBindingsMatch(existingClusterRoleBinding, desiredClusterRoleBinding) {
					delete = false
					break
				}
			}

			if delete {
				opts.logger.Info("No matching cluster role binding found, deleting", "clusterRoleBinding", existingClusterRoleBinding.Name)
				clusterRoleBindingsToDelete = append(clusterRoleBindingsToDelete, &existingClusterRoleBinding)
			}
		}
	} else if !errors.IsNotFound(err) {
		opts.logger.Error(err, "failed to list user provided permission cluster role bindings")
		return nil, err
	}

	return clusterRoleBindingsToDelete, nil
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
	return v1alpha1.UserPermissionPipelineResourcesLabels(
		pipeline.Job.GetLabels()[v1alpha1.PromiseNameLabel],
		pipeline.Name,
		pipeline.Job.Namespace,
		pipeline.Job.GetLabels()[v1alpha1.WorkTypeLabel],
		pipeline.Job.GetLabels()[v1alpha1.WorkActionLabel])
}
