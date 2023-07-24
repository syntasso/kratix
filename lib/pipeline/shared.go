package pipeline

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"github.com/syntasso/kratix/api/v1alpha1"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const kratixOperationEnvVar = "KRATIX_OPERATION"

func role(rr *unstructured.Unstructured, names apiextensionsv1.CustomResourceDefinitionNames, resources pipelineArgs) *rbacv1.Role {
	return &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			Kind: "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.RoleName(),
			Labels:    resources.Labels(),
			Namespace: resources.Namespace(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{rr.GroupVersionKind().Group},
				Resources: []string{names.Plural, names.Plural + "/status"},
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

func serviceAccount(resources pipelineArgs) *v1.ServiceAccount {
	return &v1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind: "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.ServiceAccountName(),
			Namespace: resources.Namespace(),
			Labels:    resources.Labels(),
		},
	}
}

func roleBinding(resources pipelineArgs) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind: "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      resources.RoleBindingName(),
			Labels:    resources.Labels(),
			Namespace: resources.Namespace(),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     resources.RoleName(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: resources.Namespace(),
				Name:      resources.ServiceAccountName(),
			},
		},
	}
}

func configMap(resources pipelineArgs, scheduling []v1alpha1.SchedulingConfig) (*v1.ConfigMap, error) {
	schedulingYAML, err := yaml.Marshal(scheduling)
	if err != nil {
		return nil, errors.Wrap(err, "error marshalling scheduling config to yaml")
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
			"scheduling": string(schedulingYAML),
		},
	}, nil
}

func readerContainer(rr *unstructured.Unstructured, volumeName string) v1.Container {
	resourceKindNameNamespace := fmt.Sprintf("%s.%s %s --namespace %s",
		strings.ToLower(rr.GetKind()), rr.GroupVersionKind().Group, rr.GetName(), rr.GetNamespace())

	resourceRequestCommand := fmt.Sprintf("kubectl get %s -oyaml > /output/object.yaml", resourceKindNameNamespace)
	container := v1.Container{
		Name:    "reader",
		Image:   "bitnami/kubectl:1.20.10",
		Command: []string{"sh", "-c", resourceRequestCommand},
		VolumeMounts: []v1.VolumeMount{
			{MountPath: "/output", Name: volumeName},
		},
	}

	return container
}

func pipelineName(pipelineType, promiseIdentifier string) string {
	return pipelineType + "-pipeline-" + promiseIdentifier + "-" + getShortUuid()
}

func getShortUuid() string {
	return string(uuid.NewUUID()[0:5])
}
