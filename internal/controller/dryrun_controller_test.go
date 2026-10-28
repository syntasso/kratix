package controller_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/resourceutil"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

var _ = Describe("DryRun works failure", func() {
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
