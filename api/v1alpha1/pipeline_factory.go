package v1alpha1

import (
	"fmt"
	"os"
	"strconv"
	"strings"

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

// The PipelineFactory defines the properties of the Promise pipeline.
type PipelineFactory struct {
	ID               string
	Promise          *Promise
	Pipeline         *Pipeline
	Namespace        string
	ResourceRequest  *unstructured.Unstructured
	ResourceWorkflow bool
	WorkflowAction   Action
	WorkflowType     Type
	ClusterScoped    bool
	CRDPlural        string
}

// Resources configures the job Resources for a pipeline.
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

	clusterRoles, err := p.clusterRole()
	if err != nil {
		return PipelineJobResources{}, err
	}

	clusterRoleBindings := p.clusterRoleBinding(clusterRoles, sa)

	roles, err := p.role()
	if err != nil {
		return PipelineJobResources{}, err
	}

	roleBindings := p.roleBindings(roles, clusterRoles, sa)

	return PipelineJobResources{
		Name:           p.Pipeline.GetName(),
		PipelineID:     p.ID,
		Job:            job,
		WorkflowType:   p.WorkflowType,
		WorkflowAction: p.WorkflowAction,
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
	cmName := "destination-selectors-" + p.Promise.GetName()
	if p.WorkflowType != WorkflowTypePromise && p.WorkflowType != WorkflowTypeResource {
		cmName = objectutil.GenerateDeterministicObjectName(fmt.Sprintf("%s-%s", cmName, string(p.WorkflowType)))
	}
	schedulingYAML, err := yaml.Marshal(workloadGroupScheduling)
	if err != nil {
		return nil, fmt.Errorf("error marshalling destinationSelectors to yaml: %w", err)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
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
	var objNamespace string
	objGroup := p.Promise.GroupVersionKind().Group
	objName := p.Promise.GetName()
	objVersion := p.Promise.GroupVersionKind().Version
	objKind := p.Promise.GroupVersionKind().Kind

	if p.ResourceWorkflow {
		objGroup = p.ResourceRequest.GroupVersionKind().Group
		objName = p.ResourceRequest.GetName()
		objVersion = p.ResourceRequest.GroupVersionKind().Version
		objNamespace = p.ResourceRequest.GetNamespace()
		objKind = p.ResourceRequest.GroupVersionKind().Kind
	}
	return []corev1.EnvVar{
		{Name: kratixActionEnvVar, Value: string(p.WorkflowAction)},
		{Name: kratixTypeEnvVar, Value: string(p.WorkflowType)},
		{Name: KratixPromiseNameEnvVar, Value: p.Promise.GetName()},
		{Name: kratixPipelineNameEnvVar, Value: p.Pipeline.Name},
		{Name: KratixObjectKindEnvVar, Value: objKind},
		{Name: KratixObjectGroupEnvVar, Value: objGroup},
		{Name: KratixObjectVersionEnvVar, Value: objVersion},
		{Name: KratixObjectNameEnvVar, Value: objName},
		{Name: KratixObjectNamespaceEnvVar, Value: objNamespace},
		{Name: KratixCrdPlural, Value: p.CRDPlural},
		{Name: KratixClusterScoped, Value: strconv.FormatBool(p.ClusterScoped)},
	}
}

func (p *PipelineFactory) readerContainer() corev1.Container {
	return corev1.Container{
		Name:    "reader",
		Image:   os.Getenv("PIPELINE_ADAPTER_IMG"),
		Command: []string{"/bin/pipeline-adapter"},
		Args:    []string{"reader"},
		Env:     p.defaultEnvVars(),
		VolumeMounts: []corev1.VolumeMount{
			{MountPath: "/kratix/input", Name: "shared-input"},
			{MountPath: "/kratix/output", Name: "shared-output"},
		},
		SecurityContext: kratixSecurityContext,
		ImagePullPolicy: DefaultImagePullPolicy,
		Resources:       *DefaultResourceRequirements,
	}
}

func (p *PipelineFactory) workCreatorContainer() corev1.Container {
	args := []string{
		"work-creator",
		"--input-directory", "/work-creator-files",
		"--promise-name", p.Promise.GetName(),
		"--pipeline-name", p.Pipeline.GetName(),
		"--namespace", p.Namespace,
		"--workflow-type", string(p.WorkflowType),
	}

	if p.ResourceWorkflow {
		args = append(args, "--resource-name", p.ResourceRequest.GetName())
	}

	if p.ResourceWorkflow && p.Promise.WorkflowPipelineNamespaceSet() {
		args = append(args, "--resource-namespace", p.ResourceRequest.GetNamespace())
	}

	return corev1.Container{
		Name:    "work-writer",
		Image:   os.Getenv("PIPELINE_ADAPTER_IMG"),
		Command: []string{"/bin/pipeline-adapter"},
		Args:    args,
		VolumeMounts: []corev1.VolumeMount{
			{MountPath: "/work-creator-files/input", Name: "shared-output"},
			{MountPath: "/work-creator-files/metadata", Name: "shared-metadata"},
			{MountPath: "/work-creator-files/kratix-system", Name: "promise-scheduling"}, // this volumemount is a configmap
		},
		SecurityContext: kratixSecurityContext,
		ImagePullPolicy: DefaultImagePullPolicy,
		Resources:       *DefaultResourceRequirements,
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

		if c.ImagePullPolicy == "" {
			c.ImagePullPolicy = DefaultImagePullPolicy
		}

		if c.Resources == nil {
			c.Resources = DefaultResourceRequirements
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
			Resources:       *c.Resources,
		})
	}

	return containers, volumes
}

func (p *PipelineFactory) pipelineJob(
	schedulingConfigMap *corev1.ConfigMap, serviceAccount *corev1.ServiceAccount, env []corev1.EnvVar,
) (*batchv1.Job, error) {
	obj, objHash, err := p.getObjAndHash()
	if err != nil {
		return nil, err
	}

	var imagePullSecrets []corev1.LocalObjectReference
	workCreatorPullSecrets := os.Getenv("PIPELINE_ADAPTER_PULL_SECRET")
	if workCreatorPullSecrets != "" {
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: workCreatorPullSecrets})
	}

	imagePullSecrets = append(imagePullSecrets, p.Pipeline.Spec.ImagePullSecrets...)

	readerContainer := p.readerContainer()
	pipelineContainers, pipelineVolumes := p.pipelineContainers()
	workCreatorContainer := p.workCreatorContainer()
	statusWriterContainer := p.statusWriterContainer(obj, env)

	volumes := append(p.defaultVolumes(schedulingConfigMap), pipelineVolumes...)
	nodeSelector := p.Pipeline.Spec.NodeSelector
	tolerations := p.Pipeline.Spec.Tolerations

	backoffLimit := p.Pipeline.Spec.JobOptions.BackoffLimit
	if backoffLimit == nil {
		backoffLimit = DefaultJobBackoffLimit
	}

	var initContainers []corev1.Container
	var containers []corev1.Container

	initContainers = []corev1.Container{readerContainer}
	switch p.WorkflowAction {
	case WorkflowActionConfigure:
		initContainers = append(initContainers, pipelineContainers...)
		initContainers = append(initContainers, workCreatorContainer)
		containers = []corev1.Container{statusWriterContainer}
	case WorkflowActionDelete:
		initContainers = append(initContainers, pipelineContainers[0:len(pipelineContainers)-1]...)
		containers = []corev1.Container{pipelineContainers[len(pipelineContainers)-1]}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        p.pipelineJobName(),
			Namespace:   p.Namespace,
			Labels:      p.pipelineJobLabels(objHash),
			Annotations: p.pipelineJobAnnotations(obj),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: backoffLimit,
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
					NodeSelector:       nodeSelector,
					Tolerations:        tolerations,
				},
			},
		},
	}

	// // todo: needs to understand side effect of not setting it
	// // no reference means no auto garbage collection; are we cleaning up jobs by finalizers anyways????
	if !p.ResourceWorkflow {
		if err = controllerutil.SetControllerReference(obj, job, scheme.Scheme); err != nil {
			return nil, err
		}
	}

	return job, nil
}

func (p *PipelineFactory) statusWriterContainer(obj *unstructured.Unstructured, env []corev1.EnvVar) corev1.Container {
	return corev1.Container{
		Name:    "status-writer",
		Image:   os.Getenv("PIPELINE_ADAPTER_IMG"),
		Command: []string{"/bin/pipeline-adapter"},
		Args:    []string{"update-status"},
		Env: append(env,
			corev1.EnvVar{Name: KratixObjectKindEnvVar, Value: strings.ToLower(obj.GetKind())},
			corev1.EnvVar{Name: KratixObjectGroupEnvVar, Value: obj.GroupVersionKind().Group},
			corev1.EnvVar{Name: KratixObjectVersionEnvVar, Value: obj.GroupVersionKind().Version},
			corev1.EnvVar{Name: KratixObjectNameEnvVar, Value: obj.GetName()},
			corev1.EnvVar{Name: KratixObjectNamespaceEnvVar, Value: obj.GetNamespace()},
			corev1.EnvVar{Name: KratixCrdPluralEnvVar, Value: p.CRDPlural},
			corev1.EnvVar{Name: KratixClusterScopedEnvVar, Value: strconv.FormatBool(p.ClusterScoped)},
		),
		VolumeMounts: []corev1.VolumeMount{{
			MountPath: "/work-creator-files/metadata",
			Name:      "shared-metadata",
		}},
		SecurityContext: kratixSecurityContext,
		ImagePullPolicy: DefaultImagePullPolicy,
		Resources:       *DefaultResourceRequirements,
	}
}

func (p *PipelineFactory) pipelineJobName() string {
	name := fmt.Sprintf("kratix-%s", p.Promise.GetName())

	if p.ResourceWorkflow {
		name = fmt.Sprintf("%s-%s", name, p.ResourceRequest.GetName())
		if p.Promise.Spec.Workflows.Config.PipelineNamespace != "" {
			name = fmt.Sprintf("%s-%s", name, p.ResourceRequest.GetNamespace())
		}
	}

	name = fmt.Sprintf("%s-%s", name, p.Pipeline.GetName())
	return objectutil.GenerateObjectName(name)
}

func (p *PipelineFactory) pipelineJobLabels(requestSHA string) map[string]string {
	ls := labels.Merge(
		promiseNameLabel(p.Promise.GetName()),
		workflowLabels(string(p.WorkflowType), string(p.WorkflowAction), p.Pipeline.GetName()),
	)
	ls = labels.Merge(ls, managedByKratixLabel())
	if p.ResourceWorkflow {
		ls = labels.Merge(ls, resourceNameLabel(p.ResourceRequest.GetName()))
		if p.Promise.WorkflowPipelineNamespaceSet() {
			ls = labels.Merge(ls, resourceNamespaceLabel(p.ResourceRequest.GetNamespace()))
		}
	}
	if requestSHA != "" {
		ls[KratixResourceHashLabel] = requestSHA
	}

	return labels.Merge(ls, p.Pipeline.GetLabels())
}

func (p *PipelineFactory) pipelineJobAnnotations(obj *unstructured.Unstructured) map[string]string {
	annotations := make(map[string]string)

	if obj != nil {
		annotations[JobResourceNamespaceAnnotation] = obj.GetNamespace()
		annotations[JobResourceNameAnnotation] = obj.GetName()
		annotations[JobResourceKindAnnotation] = obj.GetKind()
		annotations[JobResourceAPIVersionAnnotation] = obj.GetAPIVersion()
	}

	return annotations
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
		rules, err := p.readResourceRule()
		if err != nil {
			return nil, err
		}
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
			Rules: append(rules, rbacv1.PolicyRule{
				APIGroups: []string{GroupVersion.Group},
				Resources: []string{"works"},
				Verbs:     []string{"*"},
			}),
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

func (p *PipelineFactory) roleBindings(
	roles []rbacv1.Role, clusterRoles []rbacv1.ClusterRole, serviceAccount *corev1.ServiceAccount,
) []rbacv1.RoleBinding {
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
		ns, ok := clusterRoleLabels[UserPermissionResourceNamespaceLabel]
		if ok && ns != userPermissionResourceNamespaceLabelAll {
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

func (p *PipelineFactory) clusterRole() ([]rbacv1.ClusterRole, error) {
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

	if p.ResourceWorkflow && p.Namespace != p.ResourceRequest.GetNamespace() {
		rules, err := p.readResourceRule()
		if err != nil {
			return nil, err
		}
		clusterRoles = append(clusterRoles, rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("%s-%s", p.ID, p.ResourceRequest.GetNamespace()),
				Labels: promiseNameLabel(p.Promise.GetName()),
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: rbacv1.SchemeGroupVersion.String(),
				Kind:       "ClusterRole",
			},
			Rules: rules,
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

	return clusterRoles, nil
}

func (p *PipelineFactory) clusterRoleBinding(
	clusterRoles []rbacv1.ClusterRole, serviceAccount *corev1.ServiceAccount,
) []rbacv1.ClusterRoleBinding {
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

func (p *PipelineFactory) readResourceRule() ([]rbacv1.PolicyRule, error) {
	_, crd, err := p.Promise.GetAPI()
	if err != nil {
		return nil, err
	}
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{crd.Spec.Group},
			Resources: []string{p.CRDPlural, p.CRDPlural + "/status"},
			Verbs:     []string{"get", "list", "update", "create", "patch"},
		},
	}, nil
}
