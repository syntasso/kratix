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

	Describe("required flags", func() {
		It("fails when --input-directory is not set", func() {
			err := runWorkCreator("--promise-name", "redis", "--pipeline-name", "configure")
			Expect(err).To(MatchError(ContainSubstring(`"input-directory"`)))
		})

		It("fails when --promise-name is not set", func() {
			err := runWorkCreator("--input-directory", inputDirectory, "--pipeline-name", "configure")
			Expect(err).To(MatchError(ContainSubstring(`"promise-name"`)))
		})

		It("fails when --pipeline-name is not set", func() {
			err := runWorkCreator("--input-directory", inputDirectory, "--promise-name", "redis")
			Expect(err).To(MatchError(ContainSubstring(`"pipeline-name"`)))
		})
	})

	Describe("a resource workflow", func() {
		It("creates one Work in the given namespace", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(listWorks("team-a")).To(HaveLen(1))
		})

		It("names the Work after the promise, resource and pipeline", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Name).To(MatchRegexp(`^redis-my-redis-configure-\w{5}$`))
		})

		It("labels the Work with the promise, resource, pipeline and workflow type", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Labels).To(Equal(map[string]string{
				v1alpha1.PromiseNameLabel:  "redis",
				v1alpha1.ResourceNameLabel: "my-redis",
				v1alpha1.PipelineNameLabel: "configure",
				v1alpha1.WorkTypeLabel:     "resource",
			}))
		})

		It("sets the promise and resource name on the Work", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(work.Spec.PromiseName).To(Equal("redis"))
			Expect(work.Spec.ResourceName).To(Equal("my-redis"))
		})

		It("ships every file under the input folder, with paths relative to it", func() {
			writeFile(inputDirectory, "input/nested/namespace.yaml", namespaceManifest)

			Expect(runResourceWorkflow()).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(work.Spec.WorkloadGroups).To(HaveLen(1))
			Expect(workloadContents(work.Spec.WorkloadGroups[0])).To(Equal(map[string]string{
				"configmap.yaml":        configMapManifest,
				"nested/namespace.yaml": namespaceManifest,
			}))
		})

		It("ignores files outside the input folder", func() {
			writeFile(inputDirectory, "stray.yaml", namespaceManifest)

			Expect(runResourceWorkflow()).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(workloadContents(work.Spec.WorkloadGroups[0])).To(HaveLen(1))
			Expect(workloadContents(work.Spec.WorkloadGroups[0])).To(HaveKey("configmap.yaml"))
		})

		It("creates the Work in kratix-platform-system when --namespace is not set", func() {
			err := runWorkCreator(
				"--input-directory", inputDirectory,
				"--promise-name", "redis",
				"--pipeline-name", "configure",
				"--resource-name", "my-redis",
			)
			Expect(err).NotTo(HaveOccurred())

			Expect(listWorks(v1alpha1.SystemNamespace)).To(HaveLen(1))
		})

		It("uses the resource workflow type when --workflow-type is not set", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Labels).To(HaveKeyWithValue(v1alpha1.WorkTypeLabel, "resource"))
		})

		When("it runs again with the same flags", func() {
			It("updates the existing Work instead of creating a new one", func() {
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
		})

		When("it runs with a different pipeline name", func() {
			It("creates a separate Work", func() {
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
		})

		When("--resource-namespace is set", func() {
			It("adds the resource namespace to the Work name", func() {
				Expect(runResourceWorkflow("--resource-namespace", "my-team")).To(Succeed())

				Expect(getOnlyWork("team-a").Name).To(MatchRegexp(`^redis-my-redis-my-team-configure-\w{5}$`))
			})

			It("adds the resource namespace to the Work labels", func() {
				Expect(runResourceWorkflow("--resource-namespace", "my-team")).To(Succeed())

				Expect(getOnlyWork("team-a").Labels).To(HaveKeyWithValue(v1alpha1.ResourceNamespaceLabel, "my-team"))
			})
		})

		When("the KRATIX_DRY_RUN environment variable is true", func() {
			It("labels the Work as a dry run", func() {
				GinkgoT().Setenv(v1alpha1.KratixDryRunEnvVar, "true")

				Expect(runResourceWorkflow()).To(Succeed())

				Expect(getOnlyWork("team-a").Labels).To(HaveKeyWithValue(v1alpha1.DryRunLabel, "true"))
			})
		})
	})

	Describe("a custom resource workflow type", func() {
		It("labels the Work with the custom workflow type", func() {
			Expect(runResourceWorkflow("--workflow-type", "resource-custom-works")).To(Succeed())

			Expect(getOnlyWork("team-a").Labels).To(HaveKeyWithValue(v1alpha1.WorkTypeLabel, "resource-custom-works"))
		})

		It("names the Work like a resource Work", func() {
			Expect(runResourceWorkflow("--workflow-type", "resource-custom-works")).To(Succeed())

			Expect(getOnlyWork("team-a").Name).To(MatchRegexp(`^redis-my-redis-configure-\w{5}$`))
		})

		It("keeps its own Work next to the resource workflow Work with the same pipeline name", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			writeFile(inputDirectory, "input/configmap.yaml", namespaceManifest)
			Expect(runResourceWorkflow("--workflow-type", "resource-custom-works")).To(Succeed())

			writeFile(inputDirectory, "input/configmap.yaml", configMapManifest)
			Expect(runResourceWorkflow()).To(Succeed())

			works := listWorks("team-a")
			Expect(works).To(HaveLen(2))
			contentsByWorkflowType := map[string]map[string]string{}
			for _, work := range works {
				contentsByWorkflowType[work.Labels[v1alpha1.WorkTypeLabel]] = workloadContents(work.Spec.WorkloadGroups[0])
			}
			Expect(contentsByWorkflowType).To(Equal(map[string]map[string]string{
				"resource":              {"configmap.yaml": configMapManifest},
				"resource-custom-works": {"configmap.yaml": namespaceManifest},
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
		runPromiseWorkflow := func() error {
			return runWorkCreator(
				"--input-directory", inputDirectory,
				"--promise-name", "redis",
				"--pipeline-name", "configure",
				"--workflow-type", "promise",
			)
		}

		It("names the Work after the promise and pipeline", func() {
			Expect(runPromiseWorkflow()).To(Succeed())

			Expect(getOnlyWork(v1alpha1.SystemNamespace).Name).To(MatchRegexp(`^redis-configure-\w{5}$`))
		})

		It("labels the Work with the promise, pipeline and workflow type", func() {
			Expect(runPromiseWorkflow()).To(Succeed())

			Expect(getOnlyWork(v1alpha1.SystemNamespace).Labels).To(Equal(map[string]string{
				v1alpha1.PromiseNameLabel:  "redis",
				v1alpha1.PipelineNameLabel: "configure",
				v1alpha1.WorkTypeLabel:     "promise",
			}))
		})

		It("leaves the resource name on the Work empty", func() {
			Expect(runPromiseWorkflow()).To(Succeed())

			Expect(getOnlyWork(v1alpha1.SystemNamespace).Spec.ResourceName).To(BeEmpty())
		})
	})

	Describe("scheduling", func() {
		It("does not add destination selectors when there is no metadata or promise scheduling", func() {
			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Spec.WorkloadGroups[0].DestinationSelectors).To(BeEmpty())
		})

		It("reads destination selectors from metadata/destination-selectors.yaml", func() {
			writeFile(inputDirectory, "metadata/destination-selectors.yaml", "- matchLabels:\n    environment: production\n")

			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(
				v1alpha1.WorkloadGroupScheduling{
					MatchLabels: map[string]string{"environment": "production"},
					Source:      "resource-workflow",
				},
			))
		})

		It("puts a directory with its own destination selectors in its own workload group", func() {
			writeFile(inputDirectory, "input/special/namespace.yaml", namespaceManifest)
			writeFile(inputDirectory, "metadata/destination-selectors.yaml", "- matchLabels:\n    environment: staging\n  directory: special\n")

			Expect(runResourceWorkflow()).To(Succeed())

			work := getOnlyWork("team-a")
			Expect(work.Spec.WorkloadGroups).To(HaveLen(2))
			specialGroup := work.Spec.WorkloadGroups[0]
			Expect(specialGroup.Directory).To(Equal("special"))
			Expect(workloadContents(specialGroup)).To(Equal(map[string]string{
				"special/namespace.yaml": namespaceManifest,
			}))
			Expect(specialGroup.DestinationSelectors).To(ConsistOf(
				v1alpha1.WorkloadGroupScheduling{
					MatchLabels: map[string]string{"environment": "staging"},
					Source:      "resource-workflow",
				},
			))
			defaultGroup := work.Spec.WorkloadGroups[1]
			Expect(defaultGroup.Directory).To(Equal(v1alpha1.DefaultWorkloadGroupDirectory))
			Expect(workloadContents(defaultGroup)).To(Equal(map[string]string{
				"configmap.yaml": configMapManifest,
			}))
		})

		It("reads promise scheduling from kratix-system/promise-scheduling", func() {
			writeFile(inputDirectory, "kratix-system/promise-scheduling", "- matchLabels:\n    environment: dev\n  source: promise\n")

			Expect(runResourceWorkflow()).To(Succeed())

			Expect(getOnlyWork("team-a").Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(
				v1alpha1.WorkloadGroupScheduling{
					MatchLabels: map[string]string{"environment": "dev"},
					Source:      "promise",
				},
			))
		})

		It("fails when metadata/destination-selectors.yaml is not valid", func() {
			writeFile(inputDirectory, "metadata/destination-selectors.yaml", "not-a-list: true\n")

			err := runResourceWorkflow()

			Expect(err).To(MatchError(ContainSubstring("invalid destination-selectors.yaml")))
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
		BeforeEach(func() {
			Expect(os.MkdirAll(filepath.Join(inputDirectory, "metadata"), 0o755)).To(Succeed())
		})

		It("is written to metadata/health-definitions.yaml for a versioned resource workflow", func() {
			Expect(runResourceWorkflow("--promise-version", "v1.0.0")).To(Succeed())

			count, err := os.ReadFile(filepath.Join(inputDirectory, "metadata", "health-definitions.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(count)).To(Equal("healthDefinitions: 0\npromiseVersion: v1.0.0\n"))
		})

		It("is not written for a custom resource workflow type", func() {
			Expect(runResourceWorkflow("--promise-version", "v1.0.0", "--workflow-type", "resource-custom-works")).To(Succeed())

			_, err := os.Stat(filepath.Join(inputDirectory, "metadata", "health-definitions.yaml"))
			Expect(err).To(MatchError(os.ErrNotExist))
		})
	})

	Describe("errors", func() {
		It("fails when the input folder does not exist", func() {
			Expect(os.RemoveAll(filepath.Join(inputDirectory, "input"))).To(Succeed())

			err := runResourceWorkflow()

			Expect(err).To(HaveOccurred())
			Expect(listWorks("team-a")).To(BeEmpty())
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
