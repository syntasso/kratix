package v1alpha1

import (
	"fmt"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/objectutil"
	"gopkg.in/yaml.v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type PipelineFactory struct {
	ID               string
	Promise          *Promise
	Pipeline         *Pipeline
	Namespace        string
	ResourceRequest  *unstructured.Unstructured
	ResourceWorkflow bool
	WorkflowAction   Action
	WorkflowType     Type
}

func (p *PipelineFactory) Resources(jobEnv []corev1.EnvVar) (PipelineJobResources, error) {
	wgScheduling := p.Promise.GetWorkloadGroupScheduling()
	schedulingConfigMap, err := p.configMap(wgScheduling)
	if err != nil {
		return PipelineJobResources{}, err
	}

	sa := p.serviceAccount()

	job, err := p.pipelineJob(schedulingConfigMap, sa, jobEnv)
	if err != nil {
		return PipelineJobResources{}, err
	}

	clusterRoles := p.clusterRole()

	clusterRoleBindings := p.clusterRoleBinding(clusterRoles, sa)

	roles, err := p.role()
	if err != nil {
		return PipelineJobResources{}, err
	}

	roleBindings := p.roleBindings(roles, clusterRoles, sa)

	return PipelineJobResources{
		Name:       p.Pipeline.GetName(),
		PipelineID: p.ID,
		Job:        job,
		Shared: SharedPipelineResources{
			ServiceAccount:      sa,
			ConfigMap:           schedulingConfigMap,
			Roles:               roles,
			RoleBindings:        roleBindings,
			ClusterRoles:        clusterRoles,
			ClusterRoleBindings: clusterRoleBindings,
		},
	}, nil
}

func (p *PipelineFactory) serviceAccount() *corev1.ServiceAccount {
	serviceAccountName := p.ID
	if p.Pipeline.Spec.RBAC.ServiceAccount != "" {
		serviceAccountName = p.Pipeline.Spec.RBAC.ServiceAccount
	}
	return &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: p.Namespace,
			Labels:    promiseNameLabel(p.Promise.GetName()),
		},
	}
}

func (p *PipelineFactory) configMap(workloadGroupScheduling []WorkloadGroupScheduling) (*corev1.ConfigMap, error) {
	if p.WorkflowAction != WorkflowActionConfigure {
		return nil, nil
	}
	schedulingYAML, err := yaml.Marshal(workloadGroupScheduling)
	if err != nil {
		return nil, errors.Wrap(err, "error marshalling destinationSelectors to yaml")
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "destination-selectors-" + p.Promise.GetName(),
			Namespace: p.Namespace,
			Labels:    promiseNameLabel(p.Promise.GetName()),
		},
		Data: map[string]string{
			"destinationSelectors": string(schedulingYAML),
		},
	}, nil
}

func (p *PipelineFactory) defaultVolumes(schedulingConfigMap *corev1.ConfigMap) []corev1.Volume {
	if p.WorkflowAction != WorkflowActionConfigure {
		return []corev1.Volume{}
	}
	return []corev1.Volume{
		{
			Name: "promise-scheduling",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: schedulingConfigMap.GetName(),
					},
					Items: []corev1.KeyToPath{{
						Key:  "destinationSelectors",
						Path: "promise-scheduling",
					}},
				},
			},
		},
	}
}

func (p *PipelineFactory) defaultPipelineVolumes() ([]corev1.Volume, []corev1.VolumeMount) {
	volumes := []corev1.Volume{
		{Name: "shared-input", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "shared-output", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "shared-metadata", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	volumeMounts := []corev1.VolumeMount{
		{MountPath: "/kratix/input", Name: "shared-input", ReadOnly: true},
		{MountPath: "/kratix/output", Name: "shared-output"},
		{MountPath: "/kratix/metadata", Name: "shared-metadata"},
	}
	return volumes, volumeMounts
}

func (p *PipelineFactory) defaultEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: kratixActionEnvVar, Value: string(p.WorkflowAction)},
		{Name: kratixTypeEnvVar, Value: string(p.WorkflowType)},
		{Name: kratixPromiseEnvVar, Value: p.Promise.GetName()},
		{Name: kratixPipelineNameEnvVar, Value: p.Pipeline.Name},
	}
}

func (p *PipelineFactory) readerContainer() corev1.Container {
	kind := p.Promise.GroupVersionKind().Kind
	group := p.Promise.GroupVersionKind().Group
	name := p.Promise.GetName()

	if p.ResourceWorkflow {
		kind = p.ResourceRequest.GetKind()
		group = p.ResourceRequest.GroupVersionKind().Group
		name = p.ResourceRequest.GetName()
	}

	return corev1.Container{
		Name:    "reader",
		Image:   PipelineAdapterImage,
		Command: []string{"sh", "-c", "reader"},
		Env: []corev1.EnvVar{
			{Name: "OBJECT_KIND", Value: strings.ToLower(kind)},
			{Name: "OBJECT_GROUP", Value: group},
			{Name: "OBJECT_NAME", Value: name},
			{Name: "OBJECT_NAMESPACE", Value: p.Namespace},
			{Name: "KRATIX_WORKFLOW_TYPE", Value: string(p.WorkflowType)},
		},
		VolumeMounts: []corev1.VolumeMount{
			{MountPath: "/kratix/input", Name: "shared-input"},
			{MountPath: "/kratix/output", Name: "shared-output"},
		},
		SecurityContext: kratixSecurityContext,
	}
}

func (p *PipelineFactory) workCreatorContainer() corev1.Container {
	workCreatorCommand := "work-creator"

	args := []string{
		"-input-directory", "/work-creator-files",
		"-promise-name", p.Promise.GetName(),
		"-pipeline-name", p.Pipeline.GetName(),
		"-namespace", p.Namespace,
		"-workflow-type", string(p.WorkflowType),
	}

	if p.ResourceWorkflow {
		args = append(args, "-resource-name", p.ResourceRequest.GetName())
	}

	workCreatorCommand = fmt.Sprintf("%s %s", workCreatorCommand, strings.Join(args, " "))

	return corev1.Container{
		Name:    "work-writer",
		Image:   PipelineAdapterImage,
		Command: []string{"sh", "-c", workCreatorCommand},
		VolumeMounts: []corev1.VolumeMount{
			{MountPath: "/work-creator-files/input", Name: "shared-output"},
			{MountPath: "/work-creator-files/metadata", Name: "shared-metadata"},
			{MountPath: "/work-creator-files/kratix-system", Name: "promise-scheduling"}, // this volumemount is a configmap
		},
		SecurityContext: kratixSecurityContext,
	}
}

func (p *PipelineFactory) pipelineContainers() ([]corev1.Container, []corev1.Volume) {
	volumes, defaultVolumeMounts := p.defaultPipelineVolumes()
	pipeline := p.Pipeline
	if len(pipeline.Spec.Volumes) > 0 {
		volumes = append(volumes, pipeline.Spec.Volumes...)
	}

	var containers []corev1.Container
	kratixEnvVars := p.defaultEnvVars()

	for _, c := range pipeline.Spec.Containers {
		containerVolumeMounts := append(defaultVolumeMounts, c.VolumeMounts...)

		if c.SecurityContext == nil {
			c.SecurityContext = DefaultUserProvidedContainersSecurityContext
		}

		containers = append(containers, corev1.Container{
			Name:            c.Name,
			Image:           c.Image,
			VolumeMounts:    containerVolumeMounts,
			Args:            c.Args,
			Command:         c.Command,
			Env:             append(kratixEnvVars, c.Env...),
			EnvFrom:         c.EnvFrom,
			ImagePullPolicy: c.ImagePullPolicy,
			SecurityContext: c.SecurityContext,
		})
	}

	return containers, volumes
}

func (p *PipelineFactory) pipelineJob(schedulingConfigMap *corev1.ConfigMap, serviceAccount *corev1.ServiceAccount, env []corev1.EnvVar) (*batchv1.Job, error) {
	obj, objHash, err := p.getObjAndHash()
	if err != nil {
		return nil, err
	}

	var imagePullSecrets []corev1.LocalObjectReference
	workCreatorPullSecrets := os.Getenv("WC_PULL_SECRET")
	if workCreatorPullSecrets != "" {
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: workCreatorPullSecrets})
	}

	imagePullSecrets = append(imagePullSecrets, p.Pipeline.Spec.ImagePullSecrets...)

	readerContainer := p.readerContainer()
	pipelineContainers, pipelineVolumes := p.pipelineContainers()
	workCreatorContainer := p.workCreatorContainer()
	statusWriterContainer := p.statusWriterContainer(obj, env)

	volumes := append(p.defaultVolumes(schedulingConfigMap), pipelineVolumes...)

	var initContainers []corev1.Container
	var containers []corev1.Container

	initContainers = []corev1.Container{readerContainer}
	if p.WorkflowAction == WorkflowActionDelete {
		initContainers = append(initContainers, pipelineContainers[0:len(pipelineContainers)-1]...)
		containers = []corev1.Container{pipelineContainers[len(pipelineContainers)-1]}
	} else {
		initContainers = append(initContainers, pipelineContainers...)
		initContainers = append(initContainers, workCreatorContainer)
		containers = []corev1.Container{statusWriterContainer}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.pipelineJobName(),
			Namespace: p.Namespace,
			Labels:    p.pipelineJobLabels(objHash),
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      p.pipelineJobLabels(objHash),
					Annotations: p.Pipeline.GetAnnotations(),
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					ServiceAccountName: serviceAccount.GetName(),
					Containers:         containers,
					ImagePullSecrets:   imagePullSecrets,
					InitContainers:     initContainers,
					Volumes:            volumes,
				},
			},
		},
	}

	if err = controllerutil.SetControllerReference(obj, job, scheme.Scheme); err != nil {
		return nil, err
	}
	return job, nil
}

func (p *PipelineFactory) statusWriterContainer(obj *unstructured.Unstructured, env []corev1.EnvVar) corev1.Container {
	return corev1.Container{
		Name:    "status-writer",
		Image:   PipelineAdapterImage,
		Command: []string{"sh", "-c", "update-status"},
		Env: append(env,
			corev1.EnvVar{Name: "OBJECT_KIND", Value: strings.ToLower(obj.GetKind())},
			corev1.EnvVar{Name: "OBJECT_GROUP", Value: obj.GroupVersionKind().Group},
			corev1.EnvVar{Name: "OBJECT_NAME", Value: obj.GetName()},
			corev1.EnvVar{Name: "OBJECT_NAMESPACE", Value: p.Namespace},
		),
		VolumeMounts: []corev1.VolumeMount{{
			MountPath: "/work-creator-files/metadata",
			Name:      "shared-metadata",
		}},
		SecurityContext: kratixSecurityContext,
	}
}

func (p *PipelineFactory) pipelineJobName() string {
	name := fmt.Sprintf("kratix-%s", p.Promise.GetName())

	if p.ResourceWorkflow {
		name = fmt.Sprintf("%s-%s", name, p.ResourceRequest.GetName())
	}

	name = fmt.Sprintf("%s-%s", name, p.Pipeline.GetName())

	return objectutil.GenerateObjectName(name)
}

func (p *PipelineFactory) pipelineJobLabels(requestSHA string) map[string]string {
	ls := labels.Merge(
		promiseNameLabel(p.Promise.GetName()),
		workflowLabels(string(p.WorkflowType), string(p.WorkflowAction), p.Pipeline.GetName()),
	)
	if p.ResourceWorkflow {
		ls = labels.Merge(ls, resourceNameLabel(p.ResourceRequest.GetName()))
	}
	if requestSHA != "" {
		ls[KratixResourceHashLabel] = requestSHA
	}

	return labels.Merge(ls, p.Pipeline.GetLabels())
}

func (p *PipelineFactory) getObjAndHash() (*unstructured.Unstructured, string, error) {
	uPromise, err := p.Promise.ToUnstructured()
	if err != nil {
		return nil, "", err
	}

	promiseHash, err := hash.ComputeHashForResource(uPromise)
	if err != nil {
		return nil, "", err
	}

	if !p.ResourceWorkflow {
		return uPromise, promiseHash, nil
	}

	resourceHash, err := hash.ComputeHashForResource(p.ResourceRequest)
	if err != nil {
		return nil, "", err
	}

	return p.ResourceRequest, hash.ComputeHash(fmt.Sprintf("%s-%s", promiseHash, resourceHash)), nil
}

func (p *PipelineFactory) role() ([]rbacv1.Role, error) {
	var roles []rbacv1.Role
	if p.ResourceWorkflow {
		_, crd, err := p.Promise.GetAPI()
		if err != nil {
			return nil, err
		}
		plural := crd.Spec.Names.Plural
		roles = append(roles, rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      p.ID,
				Labels:    promiseNameLabel(p.Promise.GetName()),
				Namespace: p.Namespace,
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "Role",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{crd.Spec.Group},
					Resources: []string{plural, plural + "/status"},
					Verbs:     []string{"get", "list", "update", "create", "patch"},
				},
				{
					APIGroups: []string{GroupVersion.Group},
					Resources: []string{"works"},
					Verbs:     []string{"*"},
				},
			},
		})
	}

	if p.Pipeline.hasUserPermissions() {
		var rules []rbacv1.PolicyRule
		for _, r := range p.Pipeline.Spec.RBAC.Permissions {
			if r.ResourceNamespace == "" {
				rules = append(rules, r.PolicyRule)
			}
		}

		if len(rules) > 0 {
			roles = append(roles, rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{
					Name:      objectutil.GenerateDeterministicObjectName(p.ID),
					Namespace: p.Namespace,
					Labels:    p.userPermissionPipelineLabels(),
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: rbacv1.SchemeGroupVersion.String(),
					Kind:       "Role",
				},
				Rules: rules,
			})
		}
	}
	return roles, nil
}

func (p *PipelineFactory) roleBindings(roles []rbacv1.Role, clusterRoles []rbacv1.ClusterRole, serviceAccount *corev1.ServiceAccount) []rbacv1.RoleBinding {
	var bindings []rbacv1.RoleBinding

	for _, role := range roles {
		bindings = append(bindings, rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      role.GetName(),
				Labels:    role.Labels,
				Namespace: p.Namespace,
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "RoleBinding",
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "Role",
				APIGroup: rbacv1.GroupName,
				Name:     role.GetName(),
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      serviceAccount.GetName(),
					Namespace: serviceAccount.GetNamespace(),
				},
			},
		})
	}

	for _, clusterRole := range clusterRoles {
		clusterRoleLabels := clusterRole.GetLabels()
		if ns, ok := clusterRoleLabels[UserPermissionResourceNamespaceLabel]; ok && ns != userPermissionResourceNamespaceLabelAll {
			bindings = append(bindings, rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      objectutil.GenerateDeterministicObjectName(p.ID + "-" + serviceAccount.GetNamespace()),
					Namespace: ns,
					Labels:    clusterRoleLabels,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: rbacv1.SchemeGroupVersion.String(),
					Kind:       "RoleBinding",
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					APIGroup: rbacv1.GroupName,
					Name:     clusterRole.GetName(),
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      rbacv1.ServiceAccountKind,
						Name:      serviceAccount.GetName(),
						Namespace: serviceAccount.GetNamespace(),
					},
				},
			})
		}
	}

	return bindings
}

func (p *PipelineFactory) clusterRole() []rbacv1.ClusterRole {
	var clusterRoles []rbacv1.ClusterRole
	if !p.ResourceWorkflow {
		clusterRoles = append(clusterRoles, rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   p.ID,
				Labels: promiseNameLabel(p.Promise.GetName()),
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRole",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{GroupVersion.Group},
					Resources: []string{PromisePlural, PromisePlural + "/status", "works"},
					Verbs:     []string{"get", "list", "update", "create", "patch"},
				},
			},
		})
	}

	if p.Pipeline.hasUserPermissions() {
		namespaceRulesMap := make(map[string][]rbacv1.PolicyRule)
		for _, r := range p.Pipeline.Spec.RBAC.Permissions {
			if r.ResourceNamespace != "" {
				if _, ok := namespaceRulesMap[r.ResourceNamespace]; !ok {
					namespaceRulesMap[r.ResourceNamespace] = []rbacv1.PolicyRule{}
				}
				namespaceRulesMap[r.ResourceNamespace] = append(namespaceRulesMap[r.ResourceNamespace], r.PolicyRule)
			}
		}

		for namespace, rules := range namespaceRulesMap {
			labels := p.userPermissionPipelineLabels()
			userPermissionResourceNamespaceLabel := namespace
			labels[UserPermissionResourceNamespaceLabel] = namespace
			if namespace == "*" {
				userPermissionResourceNamespaceLabel = "kratix-all-namespaces"
				labels[UserPermissionResourceNamespaceLabel] = userPermissionResourceNamespaceLabelAll
			}

			generatedName := objectutil.GenerateDeterministicObjectName(p.ID + "-" + userPermissionResourceNamespaceLabel)

			clusterRole := rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name:   generatedName,
					Labels: labels,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: rbacv1.SchemeGroupVersion.String(),
					Kind:       "ClusterRole",
				},
				Rules: rules,
			}

			clusterRoles = append(clusterRoles, clusterRole)
		}
	}

	return clusterRoles
}

func (p *PipelineFactory) clusterRoleBinding(clusterRoles []rbacv1.ClusterRole, serviceAccount *corev1.ServiceAccount) []rbacv1.ClusterRoleBinding {
	var clusterRoleBindings []rbacv1.ClusterRoleBinding
	for _, r := range clusterRoles {
		if ns, ok := r.GetLabels()[UserPermissionResourceNamespaceLabel]; !ok {
			clusterRoleBindings = append(clusterRoleBindings, rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:   r.GetName(),
					Labels: promiseNameLabel(p.Promise.GetName()),
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: rbacv1.SchemeGroupVersion.String(),
					Kind:       "ClusterRoleBinding",
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					APIGroup: rbacv1.GroupName,
					Name:     r.GetName(),
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: serviceAccount.GetNamespace(),
						Name:      serviceAccount.GetName(),
					},
				},
			})
		} else if ns == userPermissionResourceNamespaceLabelAll {
			clusterRoleBindings = append(clusterRoleBindings, rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:   objectutil.GenerateDeterministicObjectName(p.ID + "-" + serviceAccount.GetNamespace()),
					Labels: p.userPermissionPipelineLabels(),
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: rbacv1.SchemeGroupVersion.String(),
					Kind:       "ClusterRoleBinding",
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					APIGroup: rbacv1.GroupName,
					Name:     r.GetName(),
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: serviceAccount.GetNamespace(),
						Name:      serviceAccount.GetName(),
					},
				},
			})
		}
	}
	return clusterRoleBindings
}

func (p *PipelineFactory) userPermissionPipelineLabels() map[string]string {
	return UserPermissionPipelineResourcesLabels(
		p.Promise.GetName(), p.Pipeline.GetName(), p.Namespace,
		string(p.WorkflowType), string(p.WorkflowAction))
}
