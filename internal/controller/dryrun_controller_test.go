package controller_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/objectutil"
	"github.com/syntasso/kratix/lib/resourceutil"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("DryRun", func() {
	Context("Promise version", func() {
		var (
			promise *v1alpha1.Promise
			dryRun  *v1alpha1.DryRun
		)

		BeforeEach(func() {
			promise = createPromise(promisePath)
			promise.Spec.Workflows.Config.DryRun = &v1alpha1.DryRunWorkflowConfig{Supported: true}
			createPromiseRevision(fakeK8sClient, promise, "v1.0.0")
			createPromiseRevision(fakeK8sClient, promise, "v2.0.0")

			dryRun = &v1alpha1.DryRun{
				ObjectMeta: metav1.ObjectMeta{Name: "preview", Namespace: "default"},
				Spec: v1alpha1.DryRunSpec{
					PromiseRef: v1alpha1.DryRunPromiseRef{Name: promise.Name},
					ResourceRequestRef: v1alpha1.DryRunResourceRequestRef{
						Name: "existing-request",
					},
					Resource: runtime.RawExtension{Raw: []byte(`{"size":"large"}`)},
				},
			}
		})

		DescribeTable("pins the request to the selected revision",
			func(requestedVersion, expectedVersion string) {
				dryRun.Spec.PromiseRef.Version = requestedVersion
				Expect(fakeK8sClient.Create(ctx, dryRun)).To(Succeed())

				reconciler := &controller.DryRunReconciler{
					Client: fakeK8sClient,
					Scheme: scheme.Scheme,
					Log:    ctrl.Log.WithName("DryRun"),
				}
				_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dryRun)})
				Expect(err).NotTo(HaveOccurred())
				_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dryRun)})
				Expect(err).NotTo(HaveOccurred())

				bindings := &v1alpha1.ResourceBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, client.InNamespace(dryRun.Namespace))).To(Succeed())
				Expect(bindings.Items).To(HaveLen(1))
				Expect(bindings.Items[0].Spec.Version).To(Equal(expectedVersion))
				bindings.Items[0].Labels["controller-added"] = "preserved"
				Expect(fakeK8sClient.Update(ctx, &bindings.Items[0])).To(Succeed())

				rrName := objectutil.GenerateDeterministicObjectName("kratix-dry-run-" + dryRun.Name)
				gvk, _, err := promise.GetAPI()
				Expect(err).NotTo(HaveOccurred())
				request := &unstructured.Unstructured{}
				request.SetGroupVersionKind(*gvk)
				Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: rrName, Namespace: dryRun.Namespace}, request)).NotTo(Succeed())

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(dryRun), dryRun)).To(Succeed())
				Expect(dryRun.Status.PromiseVersion).To(Equal(expectedVersion))

				createPromiseRevision(fakeK8sClient, promise, "v3.0.0")
				_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dryRun)})
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: rrName, Namespace: dryRun.Namespace}, request)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&bindings.Items[0]), &bindings.Items[0])).To(Succeed())
				Expect(bindings.Items[0].Labels).To(HaveKeyWithValue("controller-added", "preserved"))
				Expect(bindings.Items[0].Spec.Version).To(Equal(expectedVersion))
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(dryRun), dryRun)).To(Succeed())
				Expect(dryRun.Status.PromiseVersion).To(Equal(expectedVersion))
				Expect(bindings.Items[0].Spec.ResourceRef).To(Equal(v1alpha1.ResourceRef{
					Name:      rrName,
					Namespace: dryRun.Namespace,
				}))
			},
			Entry("when version is omitted", "", "v2.0.0"),
			Entry("when version is latest", "latest", "v2.0.0"),
			Entry("when an exact version is requested", "v1.0.0", "v1.0.0"),
		)

		It("waits when the created binding is not visible yet", func() {
			dryRun.Spec.PromiseRef.Version = "v1.0.0"
			Expect(fakeK8sClient.Create(ctx, dryRun)).To(Succeed())

			reconciler := &controller.DryRunReconciler{
				Client: fakeK8sClient,
				Scheme: scheme.Scheme,
				Log:    ctrl.Log.WithName("DryRun"),
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dryRun)}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			reconciler.Client = &staleResourceBindingClient{Client: fakeK8sClient, hideBindingOnce: true}
			result, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		})

		It("does not create dry run resources when the Promise revision does not support dry runs", func() {
			dryRun.Spec.PromiseRef.Version = "v1.0.0"
			Expect(fakeK8sClient.Create(ctx, dryRun)).To(Succeed())

			revisions := &v1alpha1.PromiseRevisionList{}
			Expect(fakeK8sClient.List(ctx, revisions, client.MatchingLabels{
				v1alpha1.PromiseNameLabel: promise.Name,
			})).To(Succeed())
			for i := range revisions.Items {
				if revisions.Items[i].Spec.Version == "v1.0.0" {
					revisions.Items[i].Spec.PromiseSpec.Workflows.Config.DryRun = nil
					Expect(fakeK8sClient.Update(ctx, &revisions.Items[i])).To(Succeed())
				}
			}

			reconciler := &controller.DryRunReconciler{
				Client: fakeK8sClient,
				Scheme: scheme.Scheme,
				Log:    ctrl.Log.WithName("DryRun"),
			}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dryRun)}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			bindings := &v1alpha1.ResourceBindingList{}
			Expect(fakeK8sClient.List(ctx, bindings, client.InNamespace(dryRun.Namespace))).To(Succeed())
			Expect(bindings.Items).To(BeEmpty())

			gvk, _, err := promise.GetAPI()
			Expect(err).NotTo(HaveOccurred())
			resourceRequest := &unstructured.Unstructured{}
			resourceRequest.SetGroupVersionKind(*gvk)
			requestName := objectutil.GenerateDeterministicObjectName("kratix-dry-run-" + dryRun.Name)
			err = fakeK8sClient.Get(ctx, client.ObjectKey{Name: requestName, Namespace: dryRun.Namespace}, resourceRequest)
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(dryRun), dryRun)).To(Succeed())
			Expect(dryRun.Status.PromiseVersion).To(Equal("v1.0.0"))
			Expect(dryRun.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", "Completed"),
				HaveField("Status", metav1.ConditionFalse),
				HaveField("Message", ContainSubstring("does not support dry runs")),
			)))
		})

		Describe("compound components", func() {
			createComponentOutput := func() string {
				gvk, _, err := promise.GetAPI()
				Expect(err).NotTo(HaveOccurred())
				manifest := []byte(fmt.Sprintf(`apiVersion: %s
kind: %s
metadata:
  name: component
  namespace: default
  labels:
    %s: preview
spec:
  size: small
`, gvk.GroupVersion().String(), gvk.Kind, v1alpha1.ParentResourceNameLabel))
				compressed, err := compression.CompressContent(manifest)
				Expect(err).NotTo(HaveOccurred())
				requestName := objectutil.GenerateDeterministicObjectName("kratix-dry-run-" + dryRun.Name)
				work := &v1alpha1.Work{
					ObjectMeta: metav1.ObjectMeta{Name: "component-output", Namespace: dryRun.Namespace, Labels: map[string]string{
						v1alpha1.PromiseNameLabel:  promise.Name,
						v1alpha1.ResourceNameLabel: requestName,
						v1alpha1.DryRunLabel:       "true",
					}},
					Spec: v1alpha1.WorkSpec{WorkloadGroups: []v1alpha1.WorkloadGroup{{
						Workloads: []v1alpha1.Workload{{Filepath: "component.yaml", Content: string(compressed)}},
					}}},
				}
				Expect(fakeK8sClient.Create(ctx, work)).To(Succeed())
				return requestName
			}

			It("creates a supported component DryRun pinned to the checked revision", func() {
				requestName := createComponentOutput()
				reconciler := &controller.DryRunReconciler{Client: fakeK8sClient, Scheme: scheme.Scheme}
				ensured, unsupported, err := controller.EnsureComponentDryRuns(
					ctx, reconciler, dryRun, promise.Name, requestName, dryRun.Namespace)
				Expect(err).NotTo(HaveOccurred())
				Expect(ensured).To(HaveLen(1))
				Expect(unsupported).To(BeEmpty())

				child := &v1alpha1.DryRun{}
				Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: ensured[0], Namespace: dryRun.Namespace}, child)).To(Succeed())
				Expect(child.Spec.PromiseRef).To(Equal(v1alpha1.DryRunPromiseRef{Name: promise.Name, Version: "v2.0.0"}))

				createPromiseRevision(fakeK8sClient, promise, "v3.0.0")
				_, _, err = controller.EnsureComponentDryRuns(
					ctx, reconciler, dryRun, promise.Name, requestName, dryRun.Namespace)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(child), child)).To(Succeed())
				Expect(child.Spec.PromiseRef.Version).To(Equal("v2.0.0"))
			})

			It("reports an unsupported component without creating a child DryRun", func() {
				revisions := &v1alpha1.PromiseRevisionList{}
				Expect(fakeK8sClient.List(ctx, revisions, client.MatchingLabels{
					v1alpha1.PromiseNameLabel: promise.Name,
				})).To(Succeed())
				for i := range revisions.Items {
					if revisions.Items[i].Spec.Version == "v2.0.0" {
						revisions.Items[i].Spec.PromiseSpec.Workflows.Config.DryRun = nil
						Expect(fakeK8sClient.Update(ctx, &revisions.Items[i])).To(Succeed())
					}
				}

				requestName := createComponentOutput()
				reconciler := &controller.DryRunReconciler{Client: fakeK8sClient, Scheme: scheme.Scheme}
				ensured, unsupported, err := controller.EnsureComponentDryRuns(
					ctx, reconciler, dryRun, promise.Name, requestName, dryRun.Namespace)
				Expect(err).NotTo(HaveOccurred())
				Expect(ensured).To(BeEmpty())
				Expect(unsupported).To(ConsistOf(SatisfyAll(
					HaveField("Promise", promise.Name),
					HaveField("Request", "component"),
					HaveField("Phase", v1alpha1.DryRunComponentFailed),
					HaveField("Message", ContainSubstring("does not support dry runs")),
				)))
				children := &v1alpha1.DryRunList{}
				Expect(fakeK8sClient.List(ctx, children, client.InNamespace(dryRun.Namespace))).To(Succeed())
				Expect(children.Items).To(BeEmpty())
			})
		})
	})

	Describe("works failure", func() {
		var (
			now             time.Time
			worksFailing    *clusterv1.Condition
			pipelineRunning *clusterv1.Condition
		)

		pipelineCompleted := func(ago time.Duration) *clusterv1.Condition {
			return &clusterv1.Condition{
				Type:               resourceutil.ConfigureWorkflowCompletedCondition,
				Status:             v1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(now.Add(-ago)),
			}
		}

		BeforeEach(func() {
			now = time.Now()
			worksFailing = &clusterv1.Condition{
				Type:    resourceutil.WorksSucceededCondition,
				Status:  v1.ConditionFalse,
				Message: "Some works associated with this resource failed: [work-a]",
			}
			pipelineRunning = &clusterv1.Condition{
				Type:   resourceutil.ConfigureWorkflowCompletedCondition,
				Status: v1.ConditionFalse,
				Reason: resourceutil.PipelinesInProgressReason,
			}
		})

		It("does not fail while the pipeline is still running", func() {
			Expect(controller.DryRunWorksFailure(pipelineRunning, worksFailing, now)).To(BeEmpty())
		})

		It("does not fail when works fail soon after the pipeline completed", func() {
			completed := pipelineCompleted(controller.DryRunWorksTimeout - time.Second)
			Expect(controller.DryRunWorksFailure(completed, worksFailing, now)).To(BeEmpty())
		})

		It("fails with the works message when works are still failing after the timeout", func() {
			completed := pipelineCompleted(controller.DryRunWorksTimeout + time.Second)
			Expect(controller.DryRunWorksFailure(completed, worksFailing, now)).To(Equal(
				"works not ready 2m0s after the pipeline completed: Some works associated with this resource failed: [work-a]"))
		})

		It("fails when works have reported nothing after the timeout", func() {
			completed := pipelineCompleted(controller.DryRunWorksTimeout + time.Second)
			Expect(controller.DryRunWorksFailure(completed, nil, now)).To(Equal(
				"works not ready 2m0s after the pipeline completed"))
		})
	})
})

type staleResourceBindingClient struct {
	client.Client
	hideBindingOnce bool
}

func (c *staleResourceBindingClient) Get(
	ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
) error {
	if _, isBinding := obj.(*v1alpha1.ResourceBinding); isBinding && c.hideBindingOnce {
		c.hideBindingOnce = false
		return apierrors.NewNotFound(schema.GroupResource{
			Group:    v1alpha1.GroupVersion.Group,
			Resource: "resourcebindings",
		}, key.Name)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
