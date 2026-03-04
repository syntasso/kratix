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

package controller_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("WorkflowJobPodCleanupReconciler", func() {
	var (
		ctx        context.Context
		reconciler *controller.WorkflowJobPodCleanupReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &controller.WorkflowJobPodCleanupReconciler{
			Client:              fakeK8sClient,
			Log:                 ctrl.Log.WithName("controllers").WithName("WorkflowJobPodCleanup"),
			PodTTLAfterFinished: time.Minute,
		}
	})

	When("the job has been terminal for at least the configured ttl", func() {
		It("deletes only terminal pods owned by the job", func() {
			completedAt := time.Now().Add(-2 * time.Minute)
			job := terminalJob("example-job", "default", types.UID("example-job-uid"), completedAt)
			Expect(fakeK8sClient.Create(ctx, job)).To(Succeed())

			succeededPod := jobOwnedPod("example-pod-succeeded", job, corev1.PodSucceeded)
			failedPod := jobOwnedPod("example-pod-failed", job, corev1.PodFailed)
			runningPod := jobOwnedPod("example-pod-running", job, corev1.PodRunning)

			anotherJob := terminalJob("another-job", "default", types.UID("another-job-uid"), completedAt)
			Expect(fakeK8sClient.Create(ctx, anotherJob)).To(Succeed())
			anotherJobPod := jobOwnedPod("another-job-pod", anotherJob, corev1.PodSucceeded)

			Expect(fakeK8sClient.Create(ctx, succeededPod)).To(Succeed())
			Expect(fakeK8sClient.Create(ctx, failedPod)).To(Succeed())
			Expect(fakeK8sClient.Create(ctx, runningPod)).To(Succeed())
			Expect(fakeK8sClient.Create(ctx, anotherJobPod)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(job)})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			Expect(isNotFound(ctx, client.ObjectKeyFromObject(succeededPod), &corev1.Pod{})).To(BeTrue())
			Expect(isNotFound(ctx, client.ObjectKeyFromObject(failedPod), &corev1.Pod{})).To(BeTrue())
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(runningPod), &corev1.Pod{})).To(Succeed())
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(anotherJobPod), &corev1.Pod{})).To(Succeed())
		})
	})

	When("the job ttl has not elapsed", func() {
		It("requeues after the remaining ttl and does not delete pods", func() {
			job := terminalJob("example-job", "default", types.UID("example-job-uid"), time.Now().Add(-30*time.Second))
			Expect(fakeK8sClient.Create(ctx, job)).To(Succeed())

			succeededPod := jobOwnedPod("example-pod-succeeded", job, corev1.PodSucceeded)
			Expect(fakeK8sClient.Create(ctx, succeededPod)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(job)})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 25*time.Second))
			Expect(result.RequeueAfter).To(BeNumerically("<=", 30*time.Second))
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(succeededPod), &corev1.Pod{})).To(Succeed())
		})
	})

	When("the job is not terminal", func() {
		It("does not requeue and does not delete pods", func() {
			job := nonTerminalJob("example-job", "default", types.UID("example-job-uid"))
			Expect(fakeK8sClient.Create(ctx, job)).To(Succeed())

			succeededPod := jobOwnedPod("example-pod-succeeded", job, corev1.PodSucceeded)
			Expect(fakeK8sClient.Create(ctx, succeededPod)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(job)})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(succeededPod), &corev1.Pod{})).To(Succeed())
		})
	})

	When("the job completion time is not set but terminal condition exists", func() {
		It("uses terminal condition time to evaluate ttl", func() {
			job := terminalJobWithoutCompletionTime("example-job", "default", types.UID("example-job-uid"), time.Now().Add(-2*time.Minute))
			Expect(fakeK8sClient.Create(ctx, job)).To(Succeed())

			succeededPod := jobOwnedPod("example-pod-succeeded", job, corev1.PodSucceeded)
			Expect(fakeK8sClient.Create(ctx, succeededPod)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(job)})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(isNotFound(ctx, client.ObjectKeyFromObject(succeededPod), &corev1.Pod{})).To(BeTrue())
		})
	})
})

func terminalJob(name, namespace string, uid types.UID, completedAt time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       uid,
			Labels: map[string]string{
				v1alpha1.ManagedByLabel: v1alpha1.ManagedByLabelValue,
			},
		},
		Status: batchv1.JobStatus{
			CompletionTime: &metav1.Time{Time: completedAt},
			Conditions: []batchv1.JobCondition{
				{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: completedAt},
				},
			},
		},
	}
}

func terminalJobWithoutCompletionTime(name, namespace string, uid types.UID, terminalAt time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       uid,
			Labels: map[string]string{
				v1alpha1.ManagedByLabel: v1alpha1.ManagedByLabelValue,
			},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:               batchv1.JobFailed,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.Time{Time: terminalAt},
				},
			},
		},
	}
}

func nonTerminalJob(name, namespace string, uid types.UID) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       uid,
			Labels: map[string]string{
				v1alpha1.ManagedByLabel: v1alpha1.ManagedByLabelValue,
			},
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: corev1.ConditionFalse,
				},
			},
		},
	}
}

func jobOwnedPod(name string, job *batchv1.Job, phase corev1.PodPhase) *corev1.Pod {
	controllerValue := true
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: job.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: batchv1.SchemeGroupVersion.String(),
					Kind:       "Job",
					Name:       job.GetName(),
					UID:        job.GetUID(),
					Controller: &controllerValue,
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}

func isNotFound(ctx context.Context, key client.ObjectKey, obj client.Object) bool {
	return kerrors.IsNotFound(fakeK8sClient.Get(ctx, key, obj))
}
