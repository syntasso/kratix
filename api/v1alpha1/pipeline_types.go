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
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/pipelineutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// PipelineSpec defines the desired state of Pipeline
type PipelineSpec struct {
	Containers       []Container                   `json:"containers,omitempty"`
	Volumes          []corev1.Volume               `json:"volumes,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

type Container struct {
	Name            string                 `json:"name,omitempty"`
	Image           string                 `json:"image,omitempty"`
	Args            []string               `json:"args,omitempty"`
	Command         []string               `json:"command,omitempty"`
	Env             []corev1.EnvVar        `json:"env,omitempty"`
	EnvFrom         []corev1.EnvFromSource `json:"envFrom,omitempty"`
	VolumeMounts    []corev1.VolumeMount   `json:"volumeMounts,omitempty"`
	ImagePullPolicy corev1.PullPolicy      `json:"imagePullPolicy,omitempty"`
}

// Pipeline is the Schema for the pipelines API
type Pipeline struct {
	//Note: Removed TypeMeta in order to stop the CRD generation.
	//		This is only for internal Kratix use.
	//metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PipelineSpec `json:"spec,omitempty"`
}

type pipelineWrapper struct {
	ID        string
	Promise   *Promise
	Pipeline  *Pipeline
	Log       logr.Logger
	Namespace string

	ResourceRequest *unstructured.Unstructured
	CRD             *apiextensionsv1.CustomResourceDefinition

	ClusterScope     bool
	ResourceWorkflow bool

	WorkflowAction Action
	WorkflowType   Type
}

func (p *Pipeline) ForPromise(promise *Promise, action Action, log logr.Logger) *pipelineWrapper {
	return &pipelineWrapper{
		ID:             promise.GetName() + "-promise-pipeline",
		Promise:        promise,
		Pipeline:       p,
		Log:            log,
		Namespace:      SystemNamespace,
		ClusterScope:   true,
		WorkflowType:   WorkflowTypePromise,
		WorkflowAction: action,
	}
}

func (p *Pipeline) ForResource(promise *Promise, action Action, crd *apiextensionsv1.CustomResourceDefinition, resourceRequest *unstructured.Unstructured, log logr.Logger) *pipelineWrapper {
	return &pipelineWrapper{
		ID:               promise.GetName() + "-resource-pipeline",
		Promise:          promise,
		Pipeline:         p,
		ResourceRequest:  resourceRequest,
		Log:              log,
		Namespace:        resourceRequest.GetNamespace(),
		ClusterScope:     false,
		ResourceWorkflow: true,
		CRD:              crd,
		WorkflowType:     WorkflowTypeResource,
		WorkflowAction:   action,
	}
}

func (p *pipelineWrapper) Resources(jobEnv []corev1.EnvVar) (pipelineutil.PipelineJobResources, error) {
	schedulingConfigMap, err := p.Promise.SchedulingConfigMap(p.ID, p.Namespace, p.labels())
	if err != nil {
		return nil, err
	}

	serviceAccount := p.serviceAccount()

	role := p.objectRole()
	roleBinding := p.objectRoleBinding(role.GetName(), serviceAccount)

	job, err := p.pipelineJob(schedulingConfigMap, serviceAccount, jobEnv)
	if err != nil {
		return nil, err
	}

	requiredResources := []client.Object{serviceAccount, role, roleBinding}
	if p.WorkflowAction == WorkflowActionConfigure {
		requiredResources = append(requiredResources, schedulingConfigMap)
	}

	return pipelineutil.NewPipelineObjects(p.Pipeline.GetName(), job, requiredResources), nil
}

func (p *pipelineWrapper) labels() map[string]string {
	labels := map[string]string{
		PromiseNameLabel: p.Promise.GetName(),
	}

	return labels
}

func (p *pipelineWrapper) serviceAccount() *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.ID,
			Namespace: p.Namespace,
			Labels:    p.labels(),
		},
	}
}

func (p *pipelineWrapper) objectRole() client.Object {
	if p.ClusterScope {
		return p.clusterRole()
	}
	return p.role()
}

func (p *pipelineWrapper) objectRoleBinding(roleName string, serviceAccount *corev1.ServiceAccount) client.Object {
	if p.ClusterScope {
		return p.clusterRoleBinding(roleName, serviceAccount)
	}
	return p.roleBinding(roleName, serviceAccount)
}

func (p *pipelineWrapper) role() *rbacv1.Role {
	plural := p.CRD.Spec.Names.Plural
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.ID,
			Labels:    p.labels(),
			Namespace: p.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{p.ResourceRequest.GroupVersionKind().Group},
				Resources: []string{plural, plural + "/status"},
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

func (p *pipelineWrapper) roleBinding(roleName string, serviceAccount *corev1.ServiceAccount) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      p.ID,
			Labels:    p.labels(),
			Namespace: p.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      serviceAccount.GetName(),
				Namespace: serviceAccount.GetNamespace(),
			},
		},
	}
}

func (p *pipelineWrapper) clusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   p.ID,
			Labels: p.labels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"platform.kratix.io"},
				Resources: []string{PromisePlural, PromisePlural + "/status", "works"},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
		},
	}
}

func (p *pipelineWrapper) clusterRoleBinding(clusterRoleName string, serviceAccount *corev1.ServiceAccount) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   p.ID,
			Labels: p.labels(),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     clusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: serviceAccount.GetNamespace(),
				Name:      serviceAccount.GetName(),
			},
		},
	}
}

func (p *pipelineWrapper) defaultVolumes(schedulingConfigMap *corev1.ConfigMap) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "metadata", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
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

func (p *pipelineWrapper) defaultPipelineVolumes() ([]corev1.Volume, []corev1.VolumeMount) {
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

const (
	kratixActionEnvVar  = "KRATIX_WORKFLOW_ACTION"
	kratixTypeEnvVar    = "KRATIX_WORKFLOW_TYPE"
	kratixPromiseEnvVar = "KRATIX_PROMISE_NAME"
)

func (p *pipelineWrapper) defaultEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: kratixActionEnvVar, Value: string(p.WorkflowAction)},
		{Name: kratixTypeEnvVar, Value: string(p.WorkflowType)},
		{Name: kratixPromiseEnvVar, Value: p.Promise.GetName()},
	}
}

func (p *pipelineWrapper) readerContainer() corev1.Container {
	kind := p.Promise.GroupVersionKind().Kind
	group := p.Promise.GroupVersionKind().Group
	name := p.Promise.GetName()

	if p.WorkflowType == WorkflowTypeResource {
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
	}
}

func (p *pipelineWrapper) workCreatorContainer() corev1.Container {
	workCreatorCommand := "./work-creator"

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
	}
}

func (p *pipelineWrapper) pipelineContainers() ([]corev1.Container, []corev1.Volume) {
	volumes, defaultVolumeMounts := p.defaultPipelineVolumes()
	pipeline := p.Pipeline
	if len(pipeline.Spec.Volumes) > 0 {
		volumes = append(volumes, pipeline.Spec.Volumes...)
	}

	containers := []corev1.Container{}
	kratixEnvVars := p.defaultEnvVars()
	for _, c := range pipeline.Spec.Containers {
		containerVolumeMounts := append(defaultVolumeMounts, c.VolumeMounts...)

		containers = append(containers, corev1.Container{
			Name:            c.Name,
			Image:           c.Image,
			VolumeMounts:    containerVolumeMounts,
			Args:            c.Args,
			Command:         c.Command,
			Env:             append(kratixEnvVars, c.Env...),
			EnvFrom:         c.EnvFrom,
			ImagePullPolicy: c.ImagePullPolicy,
		})
	}

	return containers, volumes
}

func GenerateObjectName(name string) string {
	const maxNameLength = 63 - 1 - 5 //five char sha, plus the separator
	if len(name) > maxNameLength {
		name = name[0 : maxNameLength-1]
	}

	id := uuid.NewUUID()

	return name + "-" + string(id[0:5])
}

func (p *pipelineWrapper) pipelineJobName() string {
	name := fmt.Sprintf("kratix-%s", p.Promise.GetName())

	if p.ResourceWorkflow {
		name = fmt.Sprintf("%s-%s", name, p.ResourceRequest.GetName())
	}

	name = fmt.Sprintf("%s-%s", name, p.Pipeline.GetName())

	return GenerateObjectName(name)
}

func (p *pipelineWrapper) pipelineJobLabels(requestSHA string) map[string]string {
	l := p.labels()

	ls := labels.Merge(l, map[string]string{
		"kratix-workflow-kind":            "pipeline.platform.kratix.io",
		"kratix-workflow-promise-version": "v1alpha1",
		"kratix-workflow-type":            string(p.WorkflowType),
		WorkTypeLabel:                     string(p.WorkflowType),
		"kratix-workflow-action":          string(p.WorkflowAction),
		"kratix-workflow-pipeline-name":   p.Pipeline.GetName(),
		PipelineNameLabel:                 p.Pipeline.GetName(),
	})

	if p.ResourceWorkflow {
		ls["kratix-promise-resource-request-id"] = p.ResourceRequest.GetName()
		ls[ResourceNameLabel] = p.ResourceRequest.GetName()
	}

	if requestSHA != "" {
		ls[KratixResourceHashLabel] = requestSHA
	}

	return ls

}

func (p *pipelineWrapper) pipelineJob(schedulingConfigMap *corev1.ConfigMap, serviceAccount *corev1.ServiceAccount, env []corev1.EnvVar) (*batchv1.Job, error) {
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
					Labels: p.pipelineJobLabels(objHash),
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
		p.Log.Error(err, "Error setting ownership")
		return nil, err
	}
	return job, nil
}

func (p *pipelineWrapper) statusWriterContainer(obj *unstructured.Unstructured, env []corev1.EnvVar) corev1.Container {
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
	}
}

func (p *pipelineWrapper) getObjAndHash() (*unstructured.Unstructured, string, error) {
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
