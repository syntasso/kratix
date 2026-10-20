package writers_test

import (
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

var _ = Describe("WriteWorkloads", func() {
	var (
		logger logr.Logger
		runner *fakeGitExecutor
		dir    string
	)

	BeforeEach(func() {
		logger = ctrl.Log.WithName("stage")
		runner = &fakeGitExecutor{root: GinkgoT().TempDir()}
		dir = filepath.Join(runner.root, "dest", "resources")
	})

	It("writes each workload under the directory and stages it", func() {
		workloads := []v1alpha1.Workload{
			{Filepath: "deployment.yaml", Content: "kind: Deployment"},
			{Filepath: "nested/service.yaml", Content: "kind: Service"},
		}

		Expect(writers.WriteWorkloads(runner, dir, workloads, logger)).To(Succeed())

		Expect(os.ReadFile(filepath.Join(dir, "deployment.yaml"))).To(Equal([]byte("kind: Deployment")))
		Expect(os.ReadFile(filepath.Join(dir, "nested", "service.yaml"))).To(Equal([]byte("kind: Service")))
		Expect(runner.added).To(ConsistOf(
			filepath.Join(dir, "deployment.yaml"),
			filepath.Join(dir, "nested", "service.yaml"),
		))
	})

	It("stages nothing when there are no workloads", func() {
		Expect(writers.WriteWorkloads(runner, dir, nil, logger)).To(Succeed())
		Expect(runner.added).To(BeEmpty())
	})

	It("refuses a workload whose path escapes the clone", func() {
		workloads := []v1alpha1.Workload{
			{Filepath: "../../../../escaped.yaml", Content: "kind: Secret"},
		}

		err := writers.WriteWorkloads(runner, dir, workloads, logger)
		Expect(err).To(MatchError(writers.ErrPathOutsideRepository))
		Expect(runner.added).To(BeEmpty())
	})
})
