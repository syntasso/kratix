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
	"bytes"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/internal/ptr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	KratixActionEnvVar       = "KRATIX_WORKFLOW_ACTION"
	KratixTypeEnvVar         = "KRATIX_WORKFLOW_TYPE"
	KratixPromiseNameEnvVar  = "KRATIX_PROMISE_NAME"
	KratixPipelineNameEnvVar = "KRATIX_PIPELINE_NAME"

	KratixObjectKindEnvVar      = "KRATIX_OBJECT_KIND"
	KratixObjectGroupEnvVar     = "KRATIX_OBJECT_GROUP"
	KratixObjectVersionEnvVar   = "KRATIX_OBJECT_VERSION"
	KratixObjectNameEnvVar      = "KRATIX_OBJECT_NAME"
	KratixObjectNamespaceEnvVar = "KRATIX_OBJECT_NAMESPACE"
	KratixCrdPluralEnvVar       = "KRATIX_CRD_PLURAL"
	KratixClusterScopedEnvVar   = "KRATIX_CLUSTER_SCOPED"

	KratixCrdPlural     = "KRATIX_CRD_PLURAL"
	KratixClusterScoped = "KRATIX_CLUSTER_SCOPED"

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
		AllowPrivilegeEscalation: ptr.False(),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		RunAsNonRoot: ptr.True(),
		SeccompProfile: &corev1.SeccompProfile{
			Type: "RuntimeDefault",
		},
		Privileged: ptr.False(),
	}
	DefaultResourceRequirements = &corev1.ResourceRequirements{
		//nolint:exhaustive
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("100m"),
			corev1.ResourceMemory:           resource.MustParse("128Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("128Mi"),
		},
		//nolint:exhaustive
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("200m"),
			corev1.ResourceMemory:           resource.MustParse("256Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("256Mi"),
		},
	}
	DefaultUserProvidedContainersSecurityContext *corev1.SecurityContext
	DefaultImagePullPolicy                       corev1.PullPolicy
	DefaultJobBackoffLimit                       *int32
)

// PipelineSpec defines the desired state of Pipeline.
type PipelineSpec struct {
	// Ordered list of OCI containers to execute as part of this pipeline
	Containers []Container `json:"containers,omitempty"`
	// Additional volumes to mount into pipeline containers
	Volumes []corev1.Volume `json:"volumes,omitempty"`
	// References to Secrets in the same namespace used for pulling container images
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	// RBAC configuration for the pipeline ServiceAccount and additional permissions
	RBAC RBAC `json:"rbac,omitempty"`
	// Options for the Kubernetes Job that runs this pipeline
	JobOptions JobOptions `json:"jobOptions,omitempty"`
	// Node selector labels for scheduling the pipeline Job Pod
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Tolerations applied to the pipeline Job Pod for scheduling on tainted nodes
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// RBAC defines the ServiceAccount and additional permissions for the pipeline
type RBAC struct {
	// Name of an existing ServiceAccount to use for the pipeline Job. If empty, Kratix creates one automatically
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// Additional RBAC permissions to grant to the pipeline ServiceAccount
	Permissions []Permission `json:"permissions,omitempty"`
}

// Permission defines an additional RBAC PolicyRule scoped to an optional namespace
type Permission struct {
	// Namespace in which this permission applies. Use "*" for all namespaces
	ResourceNamespace string `json:"resourceNamespace,omitempty"`
	rbacv1.PolicyRule `json:",inline"`
}

// JobOptions defines configuration for the Kubernetes Job that runs the pipeline
type JobOptions struct {
	// Number of retries before marking the pipeline Job as failed
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// Container defines a single pipeline step container
type Container struct {
	// Name of the container; must be unique within the pipeline
	Name string `json:"name,omitempty"`
	// OCI image reference for this container
	Image string `json:"image,omitempty"`
	// Arguments passed to the container entrypoint
	Args []string `json:"args,omitempty"`
	// Entrypoint command; replaces the image's default ENTRYPOINT
	Command []string `json:"command,omitempty"`
	// Environment variables to set in the container
	Env []corev1.EnvVar `json:"env,omitempty"`
	// Sources from which to populate environment variables in the container
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`
	// Volumes to mount into the container's filesystem
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
	// Policy for pulling the container image; defaults to IfNotPresent
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	// Security context for this container, overriding pod-level defaults
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
	// CPU and memory resource requests and limits for this container
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// Pipeline is the Schema for the pipelines API.
type Pipeline struct {
	//Note: not using TypeMeta in order to stop the CRD generation.
	//		This is only for internal Kratix use.
	Kind              string `json:"kind,omitempty" protobuf:"bytes,1,opt,name=kind"`
	APIVersion        string `json:"apiVersion,omitempty" protobuf:"bytes,2,opt,name=apiVersion"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PipelineSpec `json:"spec,omitempty"`
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

// GetObjects returns a list of the shared objects for the pipeline.
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

// PipelinesFromUnstructured converts a list of unstructured objects to Pipeline objects.
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
				fmtErr := fmt.Errorf("failed marshalling pipeline %s to json: %w", pipeline.GetName(), err)
				pipelineLogger.Error(fmtErr, "error parsing pipelines")
				return nil, fmtErr
			}
			decoder := json.NewDecoder(bytes.NewReader(jsonPipeline))
			decoder.DisallowUnknownFields()

			var p Pipeline
			if err = decoder.Decode(&p); err != nil {
				fmtErr := fmt.Errorf("failed unmarshalling pipeline %s: %w", pipeline.GetName(), err)
				pipelineLogger.Error(fmtErr, "error parsing pipelines")
				return nil, fmtErr
			}
			ps = append(ps, p)
		} else {
			return nil, fmt.Errorf("unsupported pipeline %q with APIVersion \"%s/%s\"",
				pipeline.GetName(), pipeline.GetKind(), pipeline.GetAPIVersion())
		}
	}
	return ps, nil
}

// ForPromise defines the PipelineFactory fields for a Promise.
func (p *Pipeline) ForPromise(promise *Promise, action Action) *PipelineFactory {
	namespace := SystemNamespace
	if promise.WorkflowPipelineNamespaceSet() {
		namespace = promise.Spec.Workflows.Config.PipelineNamespace
	}
	return &PipelineFactory{
		ID:             promise.GetName() + "-promise-" + string(action) + "-" + p.GetName(),
		Promise:        promise,
		Pipeline:       p,
		Namespace:      namespace,
		WorkflowType:   WorkflowTypePromise,
		WorkflowAction: action,
		ClusterScoped:  true,
		CRDPlural:      "promises",
	}
}

// ForResource defines the PipelineFactory fields for a Resource.
func (p *Pipeline) ForResource(
	promise *Promise, action Action, resourceRequest *unstructured.Unstructured,
) *PipelineFactory {
	_, crd, _ := promise.GetAPI()
	var clusterScoped bool
	var plural string
	if crd != nil {
		plural = crd.Spec.Names.Plural
		if crd.Spec.Scope == apiextensionsv1.ClusterScoped {
			clusterScoped = true
		}
	}

	namespace := resourceRequest.GetNamespace()
	if promise.WorkflowPipelineNamespaceSet() {
		namespace = promise.Spec.Workflows.Config.PipelineNamespace
	}

	return &PipelineFactory{
		ID:               promise.GetName() + "-resource-" + string(action) + "-" + p.GetName(),
		Promise:          promise,
		Pipeline:         p,
		ResourceRequest:  resourceRequest,
		Namespace:        namespace,
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

func resourceNamespaceLabel(rNamespace string) map[string]string {
	return map[string]string{
		ResourceNamespaceLabel: rNamespace,
	}
}

func managedByKratixLabel() map[string]string {
	return map[string]string{
		ManagedByLabel: ManagedByLabelValue,
	}
}
