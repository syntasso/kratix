package lib_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/work-creator/lib"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("WorkCreator", func() {
	var (
		pipelineName = "configure-job"
		promiseName  = "promise-name"
		resourceName = "resource-name"
	)

	When("WorkCreator Executes", func() {
		var (
			workCreator       lib.WorkCreator
			expectedNamespace string
		)

		BeforeEach(func() {
			expectedNamespace = "default"

			workCreator = lib.WorkCreator{
				K8sClient: k8sClient,
			}
			err := k8sClient.Create(context.Background(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kratix-platform-system"}})
			Expect(err).NotTo(HaveOccurred())
		})

		When("provided a complete set of inputs for a resource request", func() {
			var workResource v1alpha1.Work
			var mockPipelineDirectory string

			BeforeEach(func() {
				mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "default", "resource", pipelineName)
				Expect(err).ToNot(HaveOccurred())

				workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
			})

			It("has a correctly configured Work resource", func() {
				Expect(workResource.Spec.ResourceName).To(Equal("resource-name"))
			})

			It("has the expected labels", func() {
				Expect(workResource.Labels).To(Equal(map[string]string{
					"kratix.io/promise-name":  "promise-name",
					"kratix.io/resource-name": "resource-name",
					"kratix.io/pipeline-name": "configure-job",
					"kratix.io/work-type":     "resource",
				}))
			})

			It("has the expected Work name", func() {
				Expect(workResource.Name).To(MatchRegexp(`^promise-name-resource-name-configure-job-\b\w{5}\b$`))
			})

			It("has the expected workloads", func() {
				mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "default", "resource", pipelineName)
				Expect(err).ToNot(HaveOccurred())

				workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
				Expect(workResource.Spec.WorkloadGroups).To(HaveLen(2))

				var paths []string
				for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
					paths = append(paths, workload.Filepath)
				}
				Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
				for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
					fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
					Expect(err).NotTo(HaveOccurred())
					included, err := compression.InCompressedContents(workload.Content, fileContent)
					Expect(err).NotTo(HaveOccurred())
					Expect(included).To(BeTrue())
				}
			})

			When("it runs for a second time", func() {
				It("Should update the previously created work", func() {
					mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete-updated")
					err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "default", "resource", pipelineName)
					Expect(err).ToNot(HaveOccurred())

					newWorkResource := getWork(expectedNamespace, promiseName, resourceName, pipelineName)
					Expect(newWorkResource.Name).To(Equal(workResource.Name))
					Expect(newWorkResource.Spec.WorkloadGroups).To(HaveLen(2))
					paths := []string{}
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						paths = append(paths, workload.Filepath)
					}
					Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
					for _, workload := range newWorkResource.Spec.WorkloadGroups[0].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						included, err := compression.InCompressedContents(workload.Content, fileContent)
						Expect(err).NotTo(HaveOccurred())
						Expect(included).To(BeTrue())
					}
				})
			})

			Describe("the Work resource workloads list", func() {
				It("has three files", func() {
					Expect(workResource.Spec.WorkloadGroups).To(HaveLen(2))

					var paths []string
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						paths = append(paths, workload.Filepath)
					}
					Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						included, err := compression.InCompressedContents(workload.Content, fileContent)
						Expect(err).NotTo(HaveOccurred())
						Expect(included).To(BeTrue())
					}

					Expect(workResource.Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(
						v1alpha1.WorkloadGroupScheduling{
							MatchLabels: map[string]string{
								"environment": "staging",
							},
							Source: "resource-workflow",
						},
					))

					paths = []string{}
					for _, workload := range workResource.Spec.WorkloadGroups[1].Workloads {
						paths = append(paths, workload.Filepath)
					}
					Expect(paths).To(ConsistOf("configmap.yaml", "foo/bar/namespace-resource-request.yaml", "foo/multi-resource-requests.yaml"))
					for _, workload := range workResource.Spec.WorkloadGroups[1].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						included, err := compression.InCompressedContents(workload.Content, fileContent)
						Expect(err).NotTo(HaveOccurred())
						Expect(included).To(BeTrue())
					}
					Expect(workResource.Spec.WorkloadGroups[1].DestinationSelectors).To(ConsistOf(
						v1alpha1.WorkloadGroupScheduling{
							MatchLabels: map[string]string{"environment": "production", "region": "europe"},
							Source:      "resource-workflow",
						},
						v1alpha1.WorkloadGroupScheduling{
							MatchLabels: map[string]string{
								"environment": "dev",
							},
							Source: "promise",
						},
						v1alpha1.WorkloadGroupScheduling{
							MatchLabels: map[string]string{
								"workflow": "label",
							},
							Source: "promise-workflow",
						},
					))
				})
			})

			// todo: failing
			When("workflow namespace and resource namespace are different", func() {
				It("sets the right name for te work", func() {

					err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "my-a-team", "resource", pipelineName)
					Expect(err).ToNot(HaveOccurred())

					workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
					Expect(workResource.Name).To(MatchRegexp(`^promise-name-resource-name-my-a-team-configure-job-\b\w{5}\b$`))
				})
			})
		})

		Context("with empty metadata directory", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "default", "resource-name", "default", "resource", pipelineName)
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not try to apply the metadata/destination-selectors.yaml when its not present", func() {
				workResource := getWork(expectedNamespace, promiseName, resourceName, pipelineName)
				Expect(workResource.Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{
							"environment": "dev",
						},
						Source: "promise",
					},
				))
			})
		})

		Context("with empty namespace string", func() {
			BeforeEach(func() {
				expectedNamespace = "kratix-platform-system"
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "", "resource-name", "", "resource", pipelineName)
				Expect(err).NotTo(HaveOccurred())
			})

			It("creates works with the namespace 'kratix-platform-system'", func() {
				getWork(expectedNamespace, promiseName, resourceName, pipelineName)
			})
		})

		When("the default workload group contains no workloads", func() {
			var workResource v1alpha1.Work
			var mockPipelineDirectory string

			BeforeEach(func() {
				mockPipelineDirectory = filepath.Join(getRootDirectory(), "empty-default-workload-group")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "default", "resource", pipelineName)
				Expect(err).ToNot(HaveOccurred())

				workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
			})

			It("does not append the default workload group to the work", func() {
				Expect(workResource.Spec.WorkloadGroups).To(HaveLen(1))

				var paths []string
				for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
					paths = append(paths, workload.Filepath)
				}
				Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
				for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
					fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
					Expect(err).NotTo(HaveOccurred())
					included, err := compression.InCompressedContents(workload.Content, fileContent)
					Expect(err).NotTo(HaveOccurred())
					Expect(included).To(BeTrue())
				}

				Expect(workResource.Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{
							"environment": "staging",
						},
						Source: "resource-workflow",
					},
				))
			})
		})

		When("given a complete set of inputs for a Promise", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "complete-for-promise"), "promise-name", "", "", "", "promise", pipelineName)
				Expect(err).NotTo(HaveOccurred())
			})

			It("has a correctly configured Work resource", func() {
				expectedNamespace = "kratix-platform-system"
				workResource := getWork(expectedNamespace, promiseName, "", pipelineName)

				Expect(workResource.Labels).To(Equal(map[string]string{
					"kratix.io/promise-name":  "promise-name",
					"kratix.io/pipeline-name": "configure-job",
					"kratix.io/work-type":     "promise",
				}))

				Expect(workResource.Spec.ResourceName).To(Equal(""))
				Expect(workResource.Spec.WorkloadGroups[0].DestinationSelectors).To(ConsistOf(
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{
							"environment": "staging",
						},
						Source: "promise-workflow",
					},
				))

				Expect(workResource.Spec.WorkloadGroups[1].DestinationSelectors).To(ConsistOf(
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{"environment": "production", "region": "europe"},
						Source:      "promise-workflow",
					},
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{
							"environment": "dev",
						},
						Source: "promise",
					},
				))
			})
		})
	})

	Describe("parse destination selectors", func() {
		It("returns a valid destination selectors", func() {
			selectors, err := lib.ParseDestinationSelectors([]byte(`[{"matchLabels":{"env": "dev"}}]`))
			Expect(err).ToNot(HaveOccurred())
			Expect(selectors).To(HaveLen(1))
			Expect(selectors[0].MatchLabels["env"]).To(Equal("dev"))
		})

		DescribeTable("error cases", func(contents string, errMsg string) {
			_, err := lib.ParseDestinationSelectors([]byte(contents))
			Expect(err).To(MatchError(ContainSubstring(errMsg)))
		},
			Entry("no selectors can be parsed", "invalid-key: invalid-value", "invalid destination-selectors.yaml: error unmarshaling JSON"),
			Entry("there are entries with no labels", "- matchLabels:\n    env: dev\n  directory: \"bar\"\n- matchLabels:\n  env: prod", "invalid destination-selectors.yaml: entry with index 1 has no selectors"),
			Entry("multiple entries with the same directory", "- matchLabels:\n    environment: production\n    region: europe\n- matchLabels:\n    environment: staging\n  directory: baz/\n- matchLabels:\n    pci: true\n  directory: baz/\n", "duplicate entries in destination-selectors.yaml"),
			Entry("multiple entries with the same directory, one with a trailing slash and one without", "- matchLabels:\n    environment: production\n    region: europe\n  directory: foo/\n- matchLabels:\n    environment: staging\n  directory: foo\n", "duplicate entries in destination-selectors.yaml"),
			Entry("multiple entries with empty directory string", "- matchLabels:\n    environment: production\n    region: europe\n- matchLabels:\n    environment: staging\n", "duplicate entries in destination-selectors.yaml"),
			Entry("entry with a sub-directory", "- matchLabels:\n    environment: production\n    region: europe\n- matchLabels:\n    environment: staging\n  directory: foo/bar\n", "invalid directory in destination-selectors.yaml: foo/bar, sub-directories are not allowed"))
	})
})

func getRootDirectory() string {
	d, _ := filepath.Abs("../samples/")
	return d
}

func getWork(namespace, promiseName, resourceName, pipelineName string) v1alpha1.Work {
	ExpectWithOffset(1, k8sClient).NotTo(BeNil())
	works := v1alpha1.WorkList{}

	l := map[string]string{
		"kratix.io/promise-name":  promiseName,
		"kratix.io/pipeline-name": pipelineName,
	}
	if resourceName != "" {
		l["kratix.io/resource-name"] = resourceName
	}

	workSelectorLabel := labels.FormatLabels(l)
	selector, err := labels.Parse(workSelectorLabel)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	err = k8sClient.List(context.Background(), &works, &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, works.Items).To(HaveLen(1))

	return works.Items[0]
}
