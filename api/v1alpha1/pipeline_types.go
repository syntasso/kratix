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

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	kratixActionEnvVar       = "KRATIX_WORKFLOW_ACTION"
	kratixTypeEnvVar         = "KRATIX_WORKFLOW_TYPE"
	kratixPromiseEnvVar      = "KRATIX_PROMISE_NAME"
	kratixPipelineNameEnvVar = "KRATIX_PIPELINE_NAME"

	WorkflowTypeLabel   = KratixPrefix + "workflow-type"
	WorkflowActionLabel = KratixPrefix + "workflow-action"

	ManagedByLabel      = "app.kubernetes.io/managed-by"
	ManagedByLabelValue = "Kratix"

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

// +kubebuilder:object:generate=false
type PipelineJobResources struct {
	Name           string
	PipelineID     string
	Job            *batchv1.Job
	Shared         SharedPipelineResources
	WorkflowType   Type
	WorkflowAction Action
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

// PipelinesFromUnstructured converts a list of unstructured objects to Pipeline objects

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
		ClusterScoped:  true,
		CRDPlural:      "promises",
	}
}

func (p *Pipeline) ForResource(promise *Promise, action Action, resourceRequest *unstructured.Unstructured) *PipelineFactory {
	_, crd, _ := promise.GetAPI()
	var clusterScoped bool
	var plural string
	if crd != nil {
		plural = crd.Spec.Names.Plural
		if crd.Spec.Scope == apiextensionsv1.ClusterScoped {
			clusterScoped = true
		}
	}
	return &PipelineFactory{
		ID:               promise.GetName() + "-resource-" + string(action) + "-" + p.GetName(),
		Promise:          promise,
		Pipeline:         p,
		ResourceRequest:  resourceRequest,
		Namespace:        resourceRequest.GetNamespace(),
		ResourceWorkflow: true,
		WorkflowType:     WorkflowTypeResource,
		WorkflowAction:   action,
		ClusterScoped:    clusterScoped,
		CRDPlural:        plural,
	}
}

func (p *Pipeline) hasUserPermissions() bool {
	return len(p.Spec.RBAC.Permissions) > 0
}

func UserPermissionPipelineResourcesLabels(promiseName, pipelineName, pipelineNamespace, workflowType, workflowAction string) map[string]string {
	labels := labels.Merge(
		promiseNameLabel(promiseName),
		workflowLabels(workflowType, workflowAction, pipelineName),
	)
	labels[PipelineNamespaceLabel] = pipelineNamespace
	return labels
}

func workflowLabels(workflowType, workflowAction, pipelineName string) map[string]string {
	ls := map[string]string{}

	if workflowType != "" {
		ls = labels.Merge(ls, map[string]string{
			WorkflowTypeLabel: workflowType,
		})
	}

	if pipelineName != "" {
		ls = labels.Merge(ls, map[string]string{
			PipelineNameLabel: pipelineName,
		})
	}

	if workflowAction != "" {
		ls = labels.Merge(ls, map[string]string{
			WorkflowActionLabel: workflowAction,
		})
	}
	return ls
}

// TODO: this part will be deprecated when we stop using the legacy labels
func UserPermissionPipelineResourcesLegacyLabels(promiseName, pipelineName, pipelineNamespace, workflowType, workflowAction string) map[string]string {
	labels := labels.Merge(
		promiseNameLabel(promiseName),
		workflowLegacyLabels(workflowType, workflowAction, pipelineName),
	)
	labels[PipelineNamespaceLabel] = pipelineNamespace
	return labels
}

// TODO: this part will be deprecated when we stop using the legacy labels
func workflowLegacyLabels(workflowType, workflowAction, pipelineName string) map[string]string {
	ls := map[string]string{}

	if workflowType != "" {
		ls = labels.Merge(ls, map[string]string{
			WorkTypeLabel: workflowType,
		})
	}

	if pipelineName != "" {
		ls = labels.Merge(ls, map[string]string{
			PipelineNameLabel: pipelineName,
		})
	}

	if workflowAction != "" {
		ls = labels.Merge(ls, map[string]string{
			WorkActionLabel: workflowAction,
		})
	}
	return ls
}

func promiseNameLabel(promiseName string) map[string]string {
	return map[string]string{
		PromiseNameLabel: promiseName,
	}
}

func resourceNameLabel(rName string) map[string]string {
	return map[string]string{
		ResourceNameLabel: rName,
	}
}

func managedByKratixLabel() map[string]string {
	return map[string]string{
		ManagedByLabel: ManagedByLabelValue,
	}
}
