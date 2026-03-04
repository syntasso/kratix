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

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// WorkflowJobPodCleanupReconciler removes terminal Pods from terminal Kratix workflow Jobs after a configured TTL.
type WorkflowJobPodCleanupReconciler struct {
	Client              client.Client
	Log                 logr.Logger
	PodTTLAfterFinished time.Duration
}

// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete

func (r *WorkflowJobPodCleanupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "workflow-job-pod-cleanup",
		"name", req.Name,
		"namespace", req.Namespace,
	)

	job := &batchv1.Job{}
	if err := r.Client.Get(ctx, req.NamespacedName, job); err != nil {
		if kerrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !shouldReconcileWorkflowJob(job) {
		return ctrl.Result{}, nil
	}

	terminalTime, ok := getJobTerminalTime(job)
	if !ok {
		logging.Debug(logger, "job is terminal but no terminal timestamp is available; skipping pod cleanup")
		return ctrl.Result{}, nil
	}

	expirationTime := terminalTime.Add(r.PodTTLAfterFinished)
	now := time.Now()
	if now.Before(expirationTime) {
		return ctrl.Result{RequeueAfter: expirationTime.Sub(now)}, nil
	}

	podList := &corev1.PodList{}
	if err := r.Client.List(
		ctx,
		podList,
		client.InNamespace(job.GetNamespace()),
		client.MatchingLabels{batchv1.JobNameLabel: job.GetName()},
	); err != nil {
		return ctrl.Result{}, err
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		err := r.Client.Delete(ctx, pod)
		if err != nil {
			if kerrors.IsNotFound(err) {
				continue
			}
			return ctrl.Result{}, err
		}
		logging.Info(logger, "deleted terminal workflow pod", "pod", pod.GetName(), "job", job.GetName())
	}

	return ctrl.Result{}, nil
}

func (r *WorkflowJobPodCleanupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	managedByKratixPredicate, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{
			v1alpha1.ManagedByLabel: v1alpha1.ManagedByLabelValue,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build managed-by label selector predicate: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&batchv1.Job{}, builder.WithPredicates(
			terminalWorkflowJobPredicate(),
			managedByKratixPredicate,
		)).
		Complete(r)
}

func terminalWorkflowJobPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			newJob, ok := e.ObjectNew.(*batchv1.Job)
			return ok && shouldReconcileWorkflowJob(newJob)
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(event.GenericEvent) bool {
			return false
		},
	}
}

func shouldReconcileWorkflowJob(job *batchv1.Job) bool {
	if job == nil {
		return false
	}
	return isJobTerminal(job)
}

func isJobTerminal(job *batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}

		if condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed {
			return true
		}
	}
	return false
}

func getJobTerminalTime(job *batchv1.Job) (time.Time, bool) {
	if job.Status.CompletionTime != nil {
		return job.Status.CompletionTime.Time, true
	}

	var latestFailedConditionTime metav1.Time
	hasFailedConditionTime := false
	for _, condition := range job.Status.Conditions {
		// Successful Jobs should always have completionTime set.
		// If completionTime is absent, only failed Jobs can provide a terminal time via status conditions.
		if condition.Type != batchv1.JobFailed || condition.Status != corev1.ConditionTrue {
			continue
		}
		if condition.LastTransitionTime.IsZero() {
			continue
		}

		if !hasFailedConditionTime || condition.LastTransitionTime.After(latestFailedConditionTime.Time) {
			latestFailedConditionTime = condition.LastTransitionTime
			hasFailedConditionTime = true
		}
	}

	if !hasFailedConditionTime {
		return time.Time{}, false
	}
	return latestFailedConditionTime.Time, true
}

func isTerminalPod(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func isPodOwnedByJob(pod *corev1.Pod, job *batchv1.Job) bool {
	for _, ownerReference := range pod.GetOwnerReferences() {
		if ownerReference.Kind != "Job" || ownerReference.Name != job.GetName() {
			continue
		}

		if len(ownerReference.UID) == 0 || len(job.GetUID()) == 0 || ownerReference.UID == job.GetUID() {
			return true
		}
	}
	return false
}
