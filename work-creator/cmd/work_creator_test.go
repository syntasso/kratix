package cmd_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/work-creator/cmd"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const configMapManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
`

const namespaceManifest = `apiVersion: v1
kind: Namespace
metadata:
  name: my-namespace
`

var _ = Describe("work-creator command", func() {
	var (
		k8sClient      client.Client
		inputDirectory string
	)

	BeforeEach(func() {
		Expect(v1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
		k8sClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		cmd.UseClient(k8sClient)

		inputDirectory = GinkgoT().TempDir()
		writeFile(inputDirectory, "input/configmap.yaml", configMapManifest)
	})

	runWorkCreator := func(args ...string) error {
		workCreator := cmd.WorkCreatorCmd()
		workCreator.SetArgs(args)
		workCreator.SetOut(GinkgoWriter)
		workCreator.SetErr(GinkgoWriter)
		return workCreator.Execute()
	}

	runResourceWorkflow := func(extraArgs ...string) error {
		args := []string{
			"--input-directory", inputDirectory,
			"--promise-name", "redis",
			"--pipeline-name", "configure",
			"--resource-name", "my-redis",
			"--namespace", "team-a",
		}
		return runWorkCreator(append(args, extraArgs...)...)
	}

	listWorks := func(namespace string) []v1alpha1.Work {
		works := &v1alpha1.WorkList{}
		ExpectWithOffset(1, k8sClient.List(context.Background(), works, client.InNamespace(namespace))).To(Succeed())
		return works.Items
	}

	getOnlyWork := func(namespace string) v1alpha1.Work {
		works := listWorks(namespace)
		ExpectWithOffset(1, works).To(HaveLen(1))
		return works[0]
	}

	It("fails without creating a Work when a required flag is not set", func() {
		err := runWorkCreator("--promise-name", "redis", "--pipeline-name", "configure")
		Expect(err).To(MatchError(`required flag(s) "input-directory" not set`))

		err = runWorkCreator("--input-directory", inputDirectory, "--pipeline-name", "configure")
		Expect(err).To(MatchError(`required flag(s) "promise-name" not set`))

		err = runWorkCreator("--input-directory", inputDirectory, "--promise-name", "redis")
		Expect(err).To(MatchError(`required flag(s) "pipeline-name" not set`))

		Expect(listWorks(v1alpha1.SystemNamespace)).To(BeEmpty())
	})

	Describe("a resource workflow", func() {
		It("creates a Work for the resource from the files in the input folder", func() {
			writeFile(inputDirectory, "input/nested/namespace.yaml", namespaceManifest)
			writeFile(inputDirectory, "stray.yaml", namespaceManifest)

			Expect(runResourceWorkflow()).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(work.Name).To(MatchRegexp(`^redis-my-redis-configure-[0-9a-f]{5}$`))
			Expect(work.Labels).To(Equal(map[string]string{
				v1alpha1.PromiseNameLabel:  "redis",
				v1alpha1.ResourceNameLabel: "my-redis",
				v1alpha1.PipelineNameLabel: "configure",
				v1alpha1.WorkTypeLabel:     "resource",
			}))
			Expect(work.Spec.PromiseName).To(Equal("redis"))
			Expect(work.Spec.ResourceName).To(Equal("my-redis"))
			Expect(work.Spec.WorkloadGroups).To(HaveLen(1))
			Expect(workloadContents(work.Spec.WorkloadGroups[0])).To(Equal(map[string]string{
				"configmap.yaml":        configMapManifest,
				"nested/namespace.yaml": namespaceManifest,
			}))
		})

		It("creates the Work in kratix-platform-system with the resource workflow type by default", func() {
			err := runWorkCreator(
				"--input-directory", inputDirectory,
				"--promise-name", "redis",
				"--pipeline-name", "configure",
				"--resource-name", "my-redis",
			)
			Expect(err).NotTo(HaveOccurred())

			work := getOnlyWork(v1alpha1.SystemNamespace)
			Expect(work.Labels).To(HaveKeyWithValue(v1alpha1.WorkTypeLabel, "resource"))
		})

		It("updates the same Work when it runs again with the same flags", func() {
			Expect(runResourceWorkflow()).To(Succeed())
			firstWork := getOnlyWork("team-a")

			writeFile(inputDirectory, "input/configmap.yaml", namespaceManifest)
			Expect(runResourceWorkflow()).To(Succeed())

			secondWork := getOnlyWork("team-a")
			Expect(secondWork.Name).To(Equal(firstWork.Name))
			Expect(workloadContents(secondWork.Spec.WorkloadGroups[0])).To(Equal(map[string]string{
				"configmap.yaml": namespaceManifest,
			}))
		})

		It("creates a separate Work for a different pipeline name", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			err := runWorkCreator(
				"--input-directory", inputDirectory,
				"--promise-name", "redis",
				"--pipeline-name", "another-pipeline",
				"--resource-name", "my-redis",
				"--namespace", "team-a",
			)
			Expect(err).NotTo(HaveOccurred())

			Expect(listWorks("team-a")).To(HaveLen(2))
		})

		It("adds the resource namespace to the Work name and labels when --resource-namespace is set", func() {
			Expect(runResourceWorkflow("--resource-namespace", "my-team")).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(work.Name).To(MatchRegexp(`^redis-my-redis-my-team-configure-[0-9a-f]{5}$`))
			Expect(work.Labels).To(HaveKeyWithValue(v1alpha1.ResourceNamespaceLabel, "my-team"))
		})

		It("labels the Work as a dry run when KRATIX_DRY_RUN is true", func() {
			GinkgoT().Setenv(v1alpha1.KratixDryRunEnvVar, "true")

			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Labels).To(HaveKeyWithValue(v1alpha1.DryRunLabel, "true"))
		})

		It("fails without creating a Work when the input folder does not exist", func() {
			Expect(os.RemoveAll(filepath.Join(inputDirectory, "input"))).To(Succeed())

			Expect(runResourceWorkflow()).To(MatchError(ContainSubstring("no such file or directory")))
			Expect(listWorks("team-a")).To(BeEmpty())
		})
	})

	Describe("a custom resource workflow type", func() {
		It("creates its own Work next to the resource workflow Work with the same pipeline name", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			writeFile(inputDirectory, "input/configmap.yaml", namespaceManifest)
			Expect(runResourceWorkflow("--workflow-type", "resource-custom-works")).To(Succeed())

			writeFile(inputDirectory, "input/configmap.yaml", configMapManifest)
			Expect(runResourceWorkflow()).To(Succeed())

			works := listWorks("team-a")
			Expect(works).To(HaveLen(2))
			worksByWorkflowType := map[string]v1alpha1.Work{}
			for _, work := range works {
				worksByWorkflowType[work.Labels[v1alpha1.WorkTypeLabel]] = work
			}
			Expect(worksByWorkflowType).To(HaveKey("resource"))
			Expect(worksByWorkflowType).To(HaveKey("resource-custom-works"))

			customWork := worksByWorkflowType["resource-custom-works"]
			Expect(customWork.Name).To(MatchRegexp(`^redis-my-redis-configure-[0-9a-f]{5}$`))
			Expect(customWork.Labels).To(Equal(map[string]string{
				v1alpha1.PromiseNameLabel:  "redis",
				v1alpha1.ResourceNameLabel: "my-redis",
				v1alpha1.PipelineNameLabel: "configure",
				v1alpha1.WorkTypeLabel:     "resource-custom-works",
			}))
			Expect(workloadContents(customWork.Spec.WorkloadGroups[0])).To(Equal(map[string]string{
				"configmap.yaml": namespaceManifest,
			}))
			Expect(workloadContents(worksByWorkflowType["resource"].Spec.WorkloadGroups[0])).To(Equal(map[string]string{
				"configmap.yaml": configMapManifest,
			}))
		})

		It("creates a Work that Kratix finds when it cleans up the resource", func() {
			Expect(runResourceWorkflow()).To(Succeed())
			Expect(runResourceWorkflow("--workflow-type", "resource-custom-works")).To(Succeed())

			works, err := resourceutil.GetAllWorksForResource(k8sClient, "team-a", "redis", "my-redis")
			Expect(err).NotTo(HaveOccurred())
			Expect(works).To(HaveLen(2))
		})
	})

	Describe("a promise workflow", func() {
		It("creates a Work for the promise in kratix-platform-system", func() {
			err := runWorkCreator(
				"--input-directory", inputDirectory,
				"--promise-name", "redis",
				"--pipeline-name", "configure",
				"--workflow-type", "promise",
			)
			Expect(err).NotTo(HaveOccurred())

			work := getOnlyWork(v1alpha1.SystemNamespace)
			Expect(work.Name).To(MatchRegexp(`^redis-configure-[0-9a-f]{5}$`))
			Expect(work.Labels).To(Equal(map[string]string{
				v1alpha1.PromiseNameLabel:  "redis",
				v1alpha1.PipelineNameLabel: "configure",
				v1alpha1.WorkTypeLabel:     "promise",
			}))
			Expect(work.Spec.PromiseName).To(Equal("redis"))
			Expect(work.Spec.ResourceName).To(BeEmpty())
		})
	})

	Describe("scheduling", func() {
		It("adds no destination selectors when there is no metadata or promise scheduling", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Spec.WorkloadGroups[0].DestinationSelectors).To(BeEmpty())
		})

		It("schedules each workload group with the selectors from metadata/destination-selectors.yaml", func() {
			writeFile(inputDirectory, "input/special/namespace.yaml", namespaceManifest)
			writeFile(inputDirectory, "metadata/destination-selectors.yaml", `- matchLabels:
    environment: production
- matchLabels:
    environment: staging
  directory: special
`)

			Expect(runResourceWorkflow()).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(work.Spec.WorkloadGroups).To(HaveLen(2))

			specialGroup := work.Spec.WorkloadGroups[0]
			Expect(specialGroup.Directory).To(Equal("special"))
			Expect(workloadContents(specialGroup)).To(Equal(map[string]string{
				"special/namespace.yaml": namespaceManifest,
			}))
			Expect(specialGroup.DestinationSelectors).To(ConsistOf(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{"environment": "staging"},
				Source:      "resource-workflow",
			}))

			defaultGroup := work.Spec.WorkloadGroups[1]
			Expect(defaultGroup.Directory).To(Equal(v1alpha1.DefaultWorkloadGroupDirectory))
			Expect(workloadContents(defaultGroup)).To(Equal(map[string]string{
				"configmap.yaml": configMapManifest,
			}))
			Expect(defaultGroup.DestinationSelectors).To(ConsistOf(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{"environment": "production"},
				Source:      "resource-workflow",
			}))
		})

		It("adds the selectors from kratix-system/promise-scheduling", func() {
			writeFile(inputDirectory, "kratix-system/promise-scheduling", `- matchLabels:
    environment: dev
  source: promise
`)

			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{"environment": "dev"},
				Source:      "promise",
			}))
		})

		It("fails without creating a Work when metadata/destination-selectors.yaml is not valid", func() {
			writeFile(inputDirectory, "metadata/destination-selectors.yaml", "not-a-list: true\n")

			Expect(runResourceWorkflow()).To(MatchError(ContainSubstring("invalid destination-selectors.yaml")))
			Expect(listWorks("team-a")).To(BeEmpty())
		})
	})

	Describe("workflow control", func() {
		It("does not create a Work when metadata/workflow-control.yaml asks to suspend", func() {
			writeFile(inputDirectory, "metadata/workflow-control.yaml", "suspend: true\n")

			Expect(runResourceWorkflow()).To(Succeed())

			Expect(listWorks("team-a")).To(BeEmpty())
		})

		It("does not update the Work when metadata/workflow-control.yaml asks to retry", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			writeFile(inputDirectory, "input/configmap.yaml", namespaceManifest)
			writeFile(inputDirectory, "metadata/workflow-control.yaml", "retryAfter: 10m\n")
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(workloadContents(getOnlyWork("team-a").Spec.WorkloadGroups[0])).To(Equal(map[string]string{
				"configmap.yaml": configMapManifest,
			}))
		})
	})

	Describe("the health definitions count file", func() {
		var countFile string

		BeforeEach(func() {
			Expect(os.MkdirAll(filepath.Join(inputDirectory, "metadata"), 0o755)).To(Succeed())
			countFile = filepath.Join(inputDirectory, "metadata", "health-definitions.yaml")
		})

		It("is written to metadata/health-definitions.yaml for a versioned resource workflow", func() {
			Expect(runResourceWorkflow("--promise-version", "v1.0.0")).To(Succeed())

			count, err := os.ReadFile(countFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(count)).To(Equal("healthDefinitions: 0\npromiseVersion: v1.0.0\n"))
		})

		It("is not written for a custom resource workflow type", func() {
			Expect(runResourceWorkflow("--promise-version", "v1.0.0", "--workflow-type", "resource-custom-works")).To(Succeed())

			_, err := os.Stat(countFile)
			Expect(err).To(MatchError(os.ErrNotExist))
		})
	})
})

func writeFile(directory, relativePath, content string) {
	path := filepath.Join(directory, relativePath)
	ExpectWithOffset(1, os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	ExpectWithOffset(1, os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
}

func workloadContents(group v1alpha1.WorkloadGroup) map[string]string {
	contents := map[string]string{}
	for _, workload := range group.Workloads {
		content, err := compression.DecompressContent([]byte(workload.Content))
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		contents[workload.Filepath] = string(content)
	}
	return contents
}
