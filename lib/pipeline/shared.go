package pipeline

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/syntasso/kratix/api/v1alpha1"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const kratixActionEnvVar = "KRATIX_WORKFLOW_ACTION"
const kratixTypeEnvVar = "KRATIX_WORKFLOW_TYPE"

func pipelineVolumes() ([]v1.Volume, []v1.VolumeMount) {
	volumes := []v1.Volume{
		{Name: "shared-input", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}},
		{Name: "shared-output", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}},
		{Name: "shared-metadata", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}},
	}
	volumeMounts := []v1.VolumeMount{
		{MountPath: "/kratix/input", Name: "shared-input", ReadOnly: true},
		{MountPath: "/kratix/output", Name: "shared-output"},
		{MountPath: "/kratix/metadata", Name: "shared-metadata"},
	}
	return volumes, volumeMounts
}

func serviceAccount(args PipelineArgs) *v1.ServiceAccount {
	return &v1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind: "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      args.ServiceAccountName(),
			Namespace: args.Namespace(),
			Labels:    args.Labels(),
		},
	}
}

func role(obj *unstructured.Unstructured, objPluralName string, args PipelineArgs) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			Kind: "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      args.RoleName(),
			Labels:    args.Labels(),
			Namespace: args.Namespace(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{obj.GroupVersionKind().Group},
				Resources: []string{objPluralName, objPluralName + "/status"},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
			{
				APIGroups: []string{"platform.kratix.io"},
				Resources: []string{"works"},
				Verbs:     []string{"get", "update", "create", "patch"},
			},
		},
	}
}

func roleBinding(args PipelineArgs) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind: "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      args.RoleBindingName(),
			Labels:    args.Labels(),
			Namespace: args.Namespace(),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     args.RoleName(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: args.Namespace(),
				Name:      args.ServiceAccountName(),
			},
		},
	}
}

func clusterRole(args PipelineArgs) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			Kind: "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   args.RoleName(),
			Labels: args.Labels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"platform.kratix.io"},
				Resources: []string{"promises", "promises/status", "works"},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
		},
	}
}

func clusterRoleBinding(args PipelineArgs) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind: "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   args.RoleBindingName(),
			Labels: args.Labels(),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     args.RoleName(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: args.Namespace(),
				Name:      args.ServiceAccountName(),
			},
		},
	}
}

func destinationSelectorsConfigMap(resources PipelineArgs, destinationSelectors []v1alpha1.Selector) (*v1.ConfigMap, error) {
	schedulingYAML, err := yaml.Marshal(destinationSelectors)
	if err != nil {
		return nil, errors.Wrap(err, "error marshalling destinationSelectors to yaml")
	}

	return &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind: "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.ConfigMapName(),
			Namespace: resources.Namespace(),
			Labels:    resources.Labels(),
		},
		Data: map[string]string{
			"destinationSelectors": string(schedulingYAML),
		},
	}, nil
}

func readerContainer(obj *unstructured.Unstructured, volumeName string) v1.Container {
	namespace := obj.GetNamespace()
	if namespace == "" {
		// if namespace is empty it means its a unnamespaced resource, so providing
		// any value is valid for kubectl
		namespace = v1alpha1.KratixSystemNamespace
	}
	objRef := fmt.Sprintf("%s.%s %s --namespace %s", strings.ToLower(obj.GetKind()), obj.GroupVersionKind().Group, obj.GetName(), namespace)
	resourceRequestCommand := fmt.Sprintf("kubectl get %s -oyaml > /output/object.yaml; cat /output/object.yaml", objRef)

	return v1.Container{
		Name:    "reader",
		Image:   "bitnami/kubectl:1.20.10",
		Command: []string{"sh", "-c", resourceRequestCommand},
		VolumeMounts: []v1.VolumeMount{
			{MountPath: "/output", Name: volumeName},
		},
	}
}

func pipelineName(pipelineType, promiseIdentifier string) string {
	return pipelineType + "-pipeline-" + promiseIdentifier + "-" + getShortUuid()
}

func getShortUuid() string {
	return string(uuid.NewUUID()[0:5])
}
