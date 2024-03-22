package workflow_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/workflow"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var namespace = "default"

var _ = Describe("ReconcileConfigure", func() {
	//TODO move commented out tests from promise and dynamic controllers to here
	It("reconcile until all jobs are complete", func() {
		promise := v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "redis",
				Namespace: namespace,
			},
		}

		Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: "redis", Namespace: namespace}, &promise)).To(Succeed())

		obj, err := promise.ToUnstructured()
		Expect(err).NotTo(HaveOccurred())

		hash, err := hash.ComputeHashForResource(obj)
		Expect(err).NotTo(HaveOccurred())
		pipelines := []workflow.Pipeline{
			{
				//TODO test it creates these as well
				JobRequiredResources: []client.Object{},
				Job: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pipeline-1",
						Namespace: namespace,
						Labels: map[string]string{
							"unique":                        "pipeline",
							"kratix-workflow-pipeline-name": "pipeline-1",
							"kratix.io/hash":                hash,
						},
					},
				},
				Name: "pipeline-1",
			},
			{
				JobRequiredResources: []client.Object{},
				Job: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pipeline-2",
						Namespace: namespace,
						Labels: map[string]string{
							"unique":                        "pipeline",
							"kratix-workflow-pipeline-name": "pipeline-2",
							"kratix.io/hash":                hash,
						},
					},
				},
				Name: "pipeline-2",
			},
		}
		p := workflow.NewOpts(ctx, fakeK8sClient, logger, obj, pipelines, "test")

		By("creating the job for the 1st pipeline")
		complete, err := workflow.ReconcileConfigure(p)
		Expect(complete).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs()).To(HaveLen(1))

		By("waiting for the 1st pipeline to complete")
		complete, err = workflow.ReconcileConfigure(p)
		Expect(complete).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())
		jobs := listJobs()
		Expect(jobs).To(HaveLen(1))

		By("creating the job for the 2nd pipeline once the 1st is finished")
		markJobAsComplete(jobs[0].Name)
		complete, err = workflow.ReconcileConfigure(p)
		Expect(complete).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs()).To(HaveLen(2))

		By("waiting for the 2nd pipeline to complete")
		complete, err = workflow.ReconcileConfigure(p)
		Expect(complete).To(BeFalse())
		Expect(err).NotTo(HaveOccurred())
		jobs = listJobs()
		Expect(jobs).To(HaveLen(2))

		By("being complete when the 2nd pipeline is done")
		markJobAsComplete(jobs[1].Name)
		complete, err = workflow.ReconcileConfigure(p)
		Expect(complete).To(BeTrue())
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs()).To(HaveLen(2))
	})
})

var callCount = 0

func markJobAsComplete(name string) {
	callCount++
	//Fake library doesn't set timestamp, and we need it set for comparing age
	//of jobs. This ensures its set once, and only when its first created, and
	//that they differ by a large enough amont (time.Now() alone was not enough)

	job := &batchv1.Job{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, job)).To(Succeed())

	job.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Duration(callCount) * time.Minute))
	Expect(fakeK8sClient.Update(ctx, job)).To(Succeed())

	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, job)).To(Succeed())

	job.Status.Conditions = []batchv1.JobCondition{
		{
			Type:   batchv1.JobComplete,
			Status: v1.ConditionTrue,
		},
	}
	job.Status.Succeeded = 1

	Expect(fakeK8sClient.Status().Update(ctx, job)).To(Succeed())

}

func listJobs() []batchv1.Job {
	jobs := &batchv1.JobList{}
	err := fakeK8sClient.List(ctx, jobs, client.InNamespace(namespace))
	Expect(err).NotTo(HaveOccurred())
	return jobs.Items
}
