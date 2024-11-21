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
	"context"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	promiselog   = logf.Log.WithName("promise-webhook")
	k8sClientSet clientset.Interface
	k8sClient    client.Client
)

func (p *Promise) SetupWebhookWithManager(mgr ctrl.Manager, cs *clientset.Clientset, c client.Client) error {
	k8sClient = c
	k8sClientSet = cs
	return ctrl.NewWebhookManagedBy(mgr).
		For(p).
		Complete()
}

// Don't delete- breaking change
var _ webhook.Defaulter = &Promise{}

func (p *Promise) Default() {
	promiselog.Info("default", "name", p.Name)
}

func (p *Promise) ValidateCreate() (admission.Warnings, error) {
	promiselog.Info("validating promise create", "name", p.Name)
	return p.validate()
}

func (p *Promise) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	promiselog.Info("validating promise update", "name", p.Name)
	oldPromise, _ := old.(*Promise)

	warnings, err := p.validate()
	if err != nil {
		return nil, err
	}

	if err := p.validateCRDChanges(oldPromise); err != nil {
		return nil, err
	}

	return warnings, nil
}

func (p *Promise) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-promise,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=promises,verbs=create;update,versions=v1alpha1,name=vpromise.kb.io,admissionReviewVersions=v1
var _ webhook.Validator = &Promise{}

func (p *Promise) validate() ([]string, error) {
	if err := p.validateCRD(); err != nil {
		return nil, err
	}

	if err := p.validatePipelines(); err != nil {
		return nil, err
	}
	return p.validateRequiredPromisesAreAvailable(), nil
}

func (p *Promise) validatePipelines() error {
	promisePipelines, err := NewPipelinesMap(p, promiselog)
	if err != nil {
		return err
	}

	for workflowType, actionToPipelineMap := range promisePipelines {
		for workflowAction, pipelines := range actionToPipelineMap {
			pipelineNamesMap := map[string]bool{}
			for _, pipeline := range pipelines {
				if err := validatePipelineLabels(pipeline, string(workflowType), string(workflowAction)); err != nil {
					return err
				}

				_, ok := pipelineNamesMap[pipeline.GetName()]
				if ok {
					return fmt.Errorf("duplicate pipeline name %q in workflow %q action %q", pipeline.GetName(), workflowType, workflowAction)
				}
				pipelineNamesMap[pipeline.GetName()] = true
				var factory *PipelineFactory
				switch workflowType {
				case WorkflowTypeResource:
					factory = pipeline.ForResource(p, workflowAction, &unstructured.Unstructured{})
				case WorkflowTypePromise:
					factory = pipeline.ForPromise(p, workflowAction)
				}

				if len(factory.ID) > 60 {
					return fmt.Errorf("%s.%s pipeline with name %q is too long. The name is used when generating resources "+
						"for the pipeline,including the ServiceAccount which follows the format of \"%s-%s-%s-%s\", which cannot be longer than 60 characters in total",
						workflowType, workflowAction, pipeline.GetName(), p.GetName(), workflowType, workflowAction, pipeline.GetName())
				}
			}
		}
	}
	return nil
}

func (p *Promise) validateCRD() error {
	_, newCrd, err := p.GetAPI()
	if err != nil {
		if err == ErrNoAPI {
			return nil
		}
		return err
	}
	_, err = k8sClientSet.ApiextensionsV1().CustomResourceDefinitions().Create(context.TODO(), newCrd, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			existingCrd, err := k8sClientSet.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), newCrd.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}

			existingCrd.Spec = newCrd.Spec
			_, err = k8sClientSet.ApiextensionsV1().CustomResourceDefinitions().Update(context.TODO(), existingCrd, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
			if err != nil {
				return fmt.Errorf("invalid CRD changes: %w", err)
			}
			return nil
		}
		return fmt.Errorf("invalid CRD: %w", err)
	}
	return nil
}

func (p *Promise) validateRequiredPromisesAreAvailable() admission.Warnings {
	warnings := []string{}
	for _, requirement := range p.Spec.RequiredPromises {
		promiselog.Info("validating requirement", "name", p.Name, "requirement", requirement.Name, "version", requirement.Version)
		promise := &Promise{}
		err := k8sClient.Get(context.TODO(), client.ObjectKey{
			Namespace: p.Namespace,
			Name:      requirement.Name,
		}, promise)
		if err != nil {
			if errors.IsNotFound(err) {
				warnings = append(warnings, fmt.Sprintf("Required Promise %q at version %q not installed", requirement.Name, requirement.Version))
				continue
			}
			promiselog.Error(err, "failed to get requirement", "requirement", requirement.Name, "version", requirement.Version)
			continue
		}
		if promise.Status.Version != requirement.Version {
			warnings = append(warnings, fmt.Sprintf("Required Promise %q installed but not at a compatible version, want: %q have: %q", requirement.Name, requirement.Version, promise.Status.Version))
		}
	}

	if len(warnings) != 0 {
		warnings = append(warnings, "Promise will not be available until the above issue(s) is resolved")
	}
	return warnings
}

func (p *Promise) validateCRDChanges(oldPromise *Promise) error {
	_, oldCrd, errOldCrd := oldPromise.GetAPI()
	_, newCrd, errNewCrd := p.GetAPI()
	if errOldCrd == ErrNoAPI {
		return nil
	}
	if errNewCrd == ErrNoAPI {
		return fmt.Errorf("cannot remove API from existing promise")
	}

	errors := []string{}
	if oldCrd.Name != newCrd.Name {
		errors = append(errors, fmt.Sprintf("* spec.api.metadata.name: Invalid value: %q: field is immutable", newCrd.Name))
	}

	if oldCrd.Kind != newCrd.Kind {
		errors = append(errors, fmt.Sprintf("* spec.api.kind: Invalid value: %q: field is immutable", newCrd.Kind))
	}

	if oldCrd.APIVersion != newCrd.APIVersion {
		errors = append(errors, fmt.Sprintf("* spec.api.apiVersion: Invalid value: %q: field is immutable", newCrd.APIVersion))
	}

	if !reflect.DeepEqual(oldCrd.Spec.Names, newCrd.Spec.Names) {
		newNames := fmt.Sprintf(
			`{"plural": %q, "singular": %q, "kind": %q}`,
			newCrd.Spec.Names.Plural,
			newCrd.Spec.Names.Singular,
			newCrd.Spec.Names.Kind,
		)
		errors = append(errors, fmt.Sprintf("* spec.api.spec.names: Invalid value: %s: field is immutable", newNames))
	}

	if len(errors) > 0 {
		return fmt.Errorf("promises.platform.kratix.io %q was not valid:\n%s", p.Name, strings.Join(errors, "\n"))
	}
	return nil
}

func validatePipelineLabels(pipeline Pipeline, workflowType, workflowAction string) error {
	for key, value := range pipeline.GetLabels() {
		errors := validation.IsValidLabelValue(value)
		if len(errors) > 0 {
			return fmt.Errorf("invalid label value %q for key %q in workflow %q action %q: %s", value, key, workflowType, workflowAction, strings.Join(errors, ","))
		}
		errors = validation.IsQualifiedName(key)
		if len(errors) > 0 {
			return fmt.Errorf("invalid label key %q in workflow %q action %q: %s", key, workflowType, workflowAction, strings.Join(errors, ","))
		}
	}
	return nil
}
