/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/lib/objectutil"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/syntasso/kratix/lib/hash"
	"gopkg.in/yaml.v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	kratixActionEnvVar       = "KRATIX_WORKFLOW_ACTION"
	kratixTypeEnvVar         = "KRATIX_WORKFLOW_TYPE"
	kratixPromiseEnvVar      = "KRATIX_PROMISE_NAME"
	kratixPipelineNameEnvVar = "KRATIX_PIPELINE_NAME"

	// This is used to identify the * namespace case in user permissions. Kubernetes does
	// not allow * as a label value, so we use this value instead.
	// It contains underscores, which makes it an invalid namespace, so it won't conflict
	// with real user namespaces.
	userPermissionResourceNamespaceLabelAll = "kratix_all_namespaces"
)

var (
	kratixSecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: "RuntimeDefault",
		},
		Privileged: ptr.To(false),
	}

	DefaultUserProvidedContainersSecurityContext *corev1.SecurityContext
)

// PipelineSpec defines the desired state of Pipeline
type PipelineSpec struct {
	Containers       []Container                   `json:"containers,omitempty"`
	Volumes          []corev1.Volume               `json:"volumes,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	RBAC             RBAC                          `json:"rbac,omitempty"`
}

type RBAC struct {
	ServiceAccount string       `json:"serviceAccount,omitempty"`
	Permissions    []Permission `json:"permissions,omitempty"`
}

type Permission struct {
	ResourceNamespace string `json:"resourceNamespace,omitempty"`
	rbacv1.PolicyRule `json:",inline"`
}

type Container struct {
	Name            string                  `json:"name,omitempty"`
	Image           string                  `json:"image,omitempty"`
	Args            []string                `json:"args,omitempty"`
	Command         []string                `json:"command,omitempty"`
	Env             []corev1.EnvVar         `json:"env,omitempty"`
	EnvFrom         []corev1.EnvFromSource  `json:"envFrom,omitempty"`
	VolumeMounts    []corev1.VolumeMount    `json:"volumeMounts,omitempty"`
	ImagePullPolicy corev1.PullPolicy       `json:"imagePullPolicy,omitempty"`
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
}

// Pipeline is the Schema for the pipelines API
type Pipeline struct {
	//Note: Removed TypeMeta in order to stop the CRD generation.
	//		This is only for internal Kratix use.
	//metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PipelineSpec `json:"spec,omitempty"`
}

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

// +kubebuilder:object:generate=false
type PipelineJobResources struct {
	Name       string
	PipelineID string
	Job        *batchv1.Job
	Shared     SharedPipelineResources
}

type SharedPipelineResources struct {
	ServiceAccount      *corev1.ServiceAccount
	ConfigMap           *corev1.ConfigMap
	Roles               []rbacv1.Role
	RoleBindings        []rbacv1.RoleBinding
	ClusterRoles        []rbacv1.ClusterRole
	ClusterRoleBindings []rbacv1.ClusterRoleBinding
}

func (p *PipelineJobResources) GetObjects() []client.Object {
	var objs []client.Object
	if p.Shared.ServiceAccount != nil {
		objs = append(objs, p.Shared.ServiceAccount)
	}
	if p.Shared.ConfigMap != nil {
		objs = append(objs, p.Shared.ConfigMap)
	}
	for _, r := range p.Shared.Roles {
		objs = append(objs, &r)
	}
	for _, r := range p.Shared.RoleBindings {
		objs = append(objs, &r)
	}
	for _, c := range p.Shared.ClusterRoles {
		objs = append(objs, &c)
	}
	for _, c := range p.Shared.ClusterRoleBindings {
		objs = append(objs, &c)
	}
	return objs
}

func PipelinesFromUnstructured(pipelines []unstructured.Unstructured, logger logr.Logger) ([]Pipeline, error) {
	if len(pipelines) == 0 {
		return nil, nil
	}

	var ps []Pipeline
	for _, pipeline := range pipelines {
		pipelineLogger := logger.WithValues(
			"pipelineKind", pipeline.GetKind(),
			"pipelineVersion", pipeline.GetAPIVersion(),
			"pipelineName", pipeline.GetName())

		if pipeline.GetKind() == "Pipeline" && pipeline.GetAPIVersion() == "platform.kratix.io/v1alpha1" {
			jsonPipeline, err := pipeline.MarshalJSON()
			if err != nil {
				pipelineLogger.Error(err, "Failed marshalling pipeline to json")
				return nil, err
			}

			p := Pipeline{}
			err = json.Unmarshal(jsonPipeline, &p)
			if err != nil {
				pipelineLogger.Error(err, "Failed unmarshalling pipeline")
				return nil, err
			}
			ps = append(ps, p)
		} else {
			return nil, fmt.Errorf("unsupported pipeline %q with APIVersion \"%s/%s\"",
				pipeline.GetName(), pipeline.GetKind(), pipeline.GetAPIVersion())
		}
	}
	return ps, nil
}

func (p *Pipeline) ForPromise(promise *Promise, action Action) *PipelineFactory {
	return &PipelineFactory{
		ID:             promise.GetName() + "-promise-" + string(action) + "-" + p.GetName(),
		Promise:        promise,
		Pipeline:       p,
		Namespace:      SystemNamespace,
		WorkflowType:   WorkflowTypePromise,
		WorkflowAction: action,
	}
}

func (p *Pipeline) ForResource(promise *Promise, action Action, resourceRequest *unstructured.Unstructured) *PipelineFactory {
	return &PipelineFactory{
		ID:               promise.GetName() + "-resource-" + string(action) + "-" + p.GetName(),
		Promise:          promise,
		Pipeline:         p,
		ResourceRequest:  resourceRequest,
		Namespace:        resourceRequest.GetNamespace(),
		ResourceWorkflow: true,
		WorkflowType:     WorkflowTypeResource,
		WorkflowAction:   action,
	}
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
			Labels:    PromiseLabels(p.Promise),
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
			Labels:    PromiseLabels(p.Promise),
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
		Image:   os.Getenv("WC_IMG"),
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
		Image:   os.Getenv("WC_IMG"),
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

	if err := controllerutil.SetControllerReference(obj, job, scheme.Scheme); err != nil {
		return nil, err
	}
	return job, nil
}

func (p *PipelineFactory) statusWriterContainer(obj *unstructured.Unstructured, env []corev1.EnvVar) corev1.Container {
	return corev1.Container{
		Name:    "status-writer",
		Image:   os.Getenv("WC_IMG"),
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
		PromiseLabels(p.Promise),
		WorkflowLabels(p.WorkflowType, p.WorkflowAction, p.Pipeline.GetName()),
	)
	if p.ResourceWorkflow {
		ls = labels.Merge(ls, ResourceLabels(p.ResourceRequest))
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
		crd, err := p.Promise.GetAPIAsCRD()
		if err != nil {
			return nil, err
		}
		plural := crd.Spec.Names.Plural
		roles = append(roles, rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      p.ID,
				Labels:    PromiseLabels(p.Promise),
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
		labels := clusterRole.GetLabels()

		if ns, ok := labels[UserPermissionResourceNamespaceLabel]; ok && ns != userPermissionResourceNamespaceLabelAll {
			bindings = append(bindings, rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      objectutil.GenerateDeterministicObjectName(p.ID + "-" + ns),
					Namespace: ns,
					Labels:    labels,
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

func (p *Pipeline) hasUserPermissions() bool {
	return len(p.Spec.RBAC.Permissions) > 0
}

func (p *PipelineFactory) clusterRole() []rbacv1.ClusterRole {
	var clusterRoles []rbacv1.ClusterRole
	if !p.ResourceWorkflow {
		clusterRoles = append(clusterRoles, rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   p.ID,
				Labels: PromiseLabels(p.Promise),
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
					Labels: PromiseLabels(p.Promise),
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
					Name:   objectutil.GenerateDeterministicObjectName(p.ID),
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
	return labels.Merge(
		PromiseLabels(p.Promise),
		WorkflowLabels(p.WorkflowType, p.WorkflowAction, p.Pipeline.GetName()),
	)
}

func PromiseLabels(promise *Promise) map[string]string {
	return map[string]string{
		PromiseNameLabel: promise.GetName(),
	}
}

func ResourceLabels(request *unstructured.Unstructured) map[string]string {
	return map[string]string{
		ResourceNameLabel: request.GetName(),
	}
}

func WorkflowLabels(workflowType Type, workflowAction Action, pipelineName string) map[string]string {
	ls := map[string]string{}

	if workflowType != "" {
		ls = labels.Merge(ls, map[string]string{
			WorkTypeLabel: string(workflowType),
		})
	}

	if pipelineName != "" {
		ls = labels.Merge(ls, map[string]string{
			PipelineNameLabel: pipelineName,
		})
	}

	if workflowAction != "" {
		ls = labels.Merge(ls, map[string]string{
			WorkActionLabel: string(workflowAction),
		})
	}
	return ls
}
