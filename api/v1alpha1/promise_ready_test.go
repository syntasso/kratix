package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type readyCase struct {
	conditions      []metav1.Condition
	workflowsFailed int64
	expectedStatus  metav1.ConditionStatus
	expectedMessage string
}

var _ = Describe("Promise ComputeReadyCondition", func() {
	DescribeTable("derives the Ready condition", func(rc readyCase) {
		p := &platformv1alpha1.Promise{}
		p.Status.Conditions = rc.conditions
		p.Status.WorkflowsFailed = rc.workflowsFailed
		cond := p.ComputeReadyCondition()
		Expect(cond.Status).To(Equal(rc.expectedStatus))
		Expect(cond.Message).To(Equal(rc.expectedMessage))
	},
		Entry("no conditions present", readyCase{
			expectedStatus:  metav1.ConditionFalse,
			expectedMessage: "Pending",
		}),
		Entry("configure workflow incomplete", readyCase{
			conditions: []metav1.Condition{{
				Type:   string(resourceutil.ConfigureWorkflowCompletedCondition),
				Status: metav1.ConditionFalse,
			}},
			expectedStatus:  metav1.ConditionFalse,
			expectedMessage: "Pending",
		}),
		Entry("workflow failed", readyCase{
			conditions: []metav1.Condition{
				{Type: string(resourceutil.ConfigureWorkflowCompletedCondition), Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseRequirementsFulfilledType, Status: metav1.ConditionTrue},
			},
			workflowsFailed: 1,
			expectedStatus:  metav1.ConditionFalse,
			expectedMessage: "Failing",
		}),
		Entry("works not succeeded", readyCase{
			conditions: []metav1.Condition{
				{Type: string(resourceutil.ConfigureWorkflowCompletedCondition), Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseRequirementsFulfilledType, Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseWorksSucceededConditionType, Status: metav1.ConditionFalse},
			},
			expectedStatus:  metav1.ConditionFalse,
			expectedMessage: "Failing",
		}),
		Entry("works unknown", readyCase{
			conditions: []metav1.Condition{
				{Type: string(resourceutil.ConfigureWorkflowCompletedCondition), Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseRequirementsFulfilledType, Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseWorksSucceededConditionType, Status: metav1.ConditionUnknown},
			},
			expectedStatus:  metav1.ConditionFalse,
			expectedMessage: "Pending",
		}),
		Entry("misplaced resources", readyCase{
			conditions: []metav1.Condition{
				{Type: string(resourceutil.ConfigureWorkflowCompletedCondition), Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseRequirementsFulfilledType, Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseWorksSucceededConditionType, Status: metav1.ConditionTrue},
				{Type: "ResourcesMisplaced", Status: metav1.ConditionTrue, Reason: "Misplaced", Message: "Misplaced"},
			},
			expectedStatus:  metav1.ConditionTrue,
			expectedMessage: "Misplaced",
		}),
		Entry("all success", readyCase{
			conditions: []metav1.Condition{
				{Type: string(resourceutil.ConfigureWorkflowCompletedCondition), Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseRequirementsFulfilledType, Status: metav1.ConditionTrue},
				{Type: platformv1alpha1.PromiseWorksSucceededConditionType, Status: metav1.ConditionTrue},
			},
			expectedStatus:  metav1.ConditionTrue,
			expectedMessage: "Requested",
		}),
	)
})
