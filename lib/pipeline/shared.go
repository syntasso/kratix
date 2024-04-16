package pipeline

import (
	"os"
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

const (
	kratixActionEnvVar  = "KRATIX_WORKFLOW_ACTION"
	kratixTypeEnvVar    = "KRATIX_WORKFLOW_TYPE"
	kratixPromiseEnvVar = "KRATIX_PROMISE_NAME"
)

func defaultPipelineVolumes() ([]v1.Volume, []v1.VolumeMount) {
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      args.ServiceAccountName(),
			Namespace: args.Namespace(),
			Labels:    args.Labels(),
		},
	}
}

func role(obj *unstructured.Unstructured, objPluralName string, args PipelineArgs) *rbacv1.Role {
	return &rbacv1.Role{
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
				Verbs:     []string{"*"},
			},
		},
	}
}

func roleBinding(args PipelineArgs) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
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
		ObjectMeta: metav1.ObjectMeta{
			Name:   args.RoleName(),
			Labels: args.Labels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"platform.kratix.io"},
				Resources: []string{v1alpha1.PromisePlural, v1alpha1.PromisePlural + "/status", "works"},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
		},
	}
}

func clusterRoleBinding(args PipelineArgs) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
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

func destinationSelectorsConfigMap(resources PipelineArgs, destinationSelectors []v1alpha1.PromiseScheduling, promiseWorkflowSelectors *v1alpha1.WorkloadGroupScheduling) (*v1.ConfigMap, error) {
	workloadGroupScheduling := []v1alpha1.WorkloadGroupScheduling{}
	for _, scheduling := range destinationSelectors {
		workloadGroupScheduling = append(workloadGroupScheduling, v1alpha1.WorkloadGroupScheduling{
			MatchLabels: scheduling.MatchLabels,
			Source:      "promise",
		})
	}

	if promiseWorkflowSelectors != nil {
		workloadGroupScheduling = append(workloadGroupScheduling, *promiseWorkflowSelectors)
	}

	schedulingYAML, err := yaml.Marshal(workloadGroupScheduling)
	if err != nil {
		return nil, errors.Wrap(err, "error marshalling destinationSelectors to yaml")
	}

	return &v1.ConfigMap{
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

func readerContainer(obj *unstructured.Unstructured, kratixWorkflowType v1alpha1.Type, volumeName string) v1.Container {
	namespace := obj.GetNamespace()
	if namespace == "" {
		// if namespace is empty it means its a unnamespaced resource, so providing
		// any value is valid for kubectl
		namespace = v1alpha1.SystemNamespace
	}

	readerContainer := v1.Container{
		Name:  "reader",
		Image: os.Getenv("WC_IMG"),
		Env: []v1.EnvVar{
			{Name: "OBJECT_KIND", Value: strings.ToLower(obj.GetKind())},
			{Name: "OBJECT_GROUP", Value: obj.GroupVersionKind().Group},
			{Name: "OBJECT_NAME", Value: obj.GetName()},
			{Name: "OBJECT_NAMESPACE", Value: namespace},
			{Name: "KRATIX_WORKFLOW_TYPE", Value: string(kratixWorkflowType)},
		},
		VolumeMounts: []v1.VolumeMount{
			{MountPath: "/kratix/input", Name: "shared-input"},
			{MountPath: "/kratix/output", Name: "shared-output"},
		},
		Command: []string{"sh", "-c", "reader"},
	}
	return readerContainer
}

func generateContainersAndVolumes(obj *unstructured.Unstructured, workflowType v1alpha1.Type, pipeline v1alpha1.Pipeline, kratixEnvVars []v1.EnvVar) ([]v1.Container, []v1.Volume) {
	volumes, volumeMounts := defaultPipelineVolumes()

	readerContainer := readerContainer(obj, workflowType, "shared-input")
	containers := []v1.Container{
		readerContainer,
	}

	if len(pipeline.Spec.Volumes) > 0 {
		volumes = append(volumes, pipeline.Spec.Volumes...)
	}
	for _, c := range pipeline.Spec.Containers {
		if len(c.VolumeMounts) > 0 {
			volumeMounts = append(volumeMounts, c.VolumeMounts...)
		}

		containers = append(containers, v1.Container{
			Name:            c.Name,
			Image:           c.Image,
			VolumeMounts:    volumeMounts,
			Args:            c.Args,
			Command:         c.Command,
			Env:             append(kratixEnvVars, c.Env...),
			EnvFrom:         c.EnvFrom,
			ImagePullPolicy: c.ImagePullPolicy,
		})
	}

	return containers, volumes
}

// TODO(breaking) change this to {promiseIdentifier}-{pipelineType}-pipeline-{short-uuid}
// for consistency with other resource names (e.g. service account)
func pipelineName(pipelineType, promiseIdentifier string) string {
	return pipelineType + "-pipeline-" + promiseIdentifier + "-" + getShortUuid()
}

func getShortUuid() string {
	return string(uuid.NewUUID()[0:5])
}
