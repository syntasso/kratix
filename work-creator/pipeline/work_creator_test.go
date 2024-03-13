package pipeline_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
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
			workCreator       pipeline.WorkCreator
			expectedNamespace string
		)

		BeforeEach(func() {
			expectedNamespace = "default"

			workCreator = pipeline.WorkCreator{
				K8sClient: k8sClient,
			}
			k8sClient.Create(context.Background(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kratix-platform-system"}})
		})

		When("provided a complete set of inputs for a resource request", func() {
			var workResource v1alpha1.Work
			var mockPipelineDirectory string

			BeforeEach(func() {
				mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
				Expect(err).ToNot(HaveOccurred())

				workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
			})

			It("has a correctly configured Work resource", func() {
				Expect(workResource.Spec.Replicas).To(Equal(1))
			})

			It("has the expected labels", func() {
				Expect(workResource.Labels).To(Equal(map[string]string{
					"kratix.io/promise-name":  "promise-name",
					"kratix.io/resource-name": "resource-name",
					"kratix.io/pipeline-name": "configure-job",
				}))
			})

			It("has the expected Work name", func() {
				Expect(workResource.Name).To(MatchRegexp(`^promise-name-resource-name-\b\w{5}\b$`))
			})

			When("it runs for a second time", func() {
				It("Should update the previously created work", func() {
					mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete")
					err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
					Expect(err).ToNot(HaveOccurred())

					workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
					Expect(workResource.Spec.WorkloadGroups).To(HaveLen(2))

					paths := []string{}
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						paths = append(paths, workload.Filepath)
					}
					Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						Expect(workload.Content).To(Equal(string(fileContent)))
					}

					mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete-updated")
					err = workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
					Expect(err).ToNot(HaveOccurred())

					newWorkResource := getWork(expectedNamespace, promiseName, resourceName, pipelineName)
					Expect(newWorkResource.Name).To(Equal(workResource.Name))
					Expect(newWorkResource.Spec.WorkloadGroups).To(HaveLen(2))
					paths = []string{}
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						paths = append(paths, workload.Filepath)
					}
					Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
					for _, workload := range newWorkResource.Spec.WorkloadGroups[0].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						Expect(workload.Content).To(Equal(string(fileContent)))
					}
				})
			})

			Describe("the Work resource workloads list", func() {
				It("has three files", func() {
					Expect(workResource.Spec.WorkloadGroups).To(HaveLen(2))

					paths := []string{}
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						paths = append(paths, workload.Filepath)
					}
					Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						Expect(workload.Content).To(Equal(string(fileContent)))
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
						Expect(workload.Content).To(Equal(string(fileContent)))
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
		})

		When("the destination-selectors contain multiple entries for the same directory", func() {
			It("errors", func() {
				mockPipelineDirectory := filepath.Join(getRootDirectory(), "duplicate-destination-selectors")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
				Expect(err).To(MatchError(ContainSubstring("duplicate entries in destination-selectors.yaml")))
			})

			When("and the directory is empty string", func() {
				It("errors", func() {
					mockPipelineDirectory := filepath.Join(getRootDirectory(), "duplicate-destination-selectors-with-empty-directory")
					err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
					Expect(err).To(MatchError(ContainSubstring("duplicate entries in destination-selectors.yaml")))
				})
			})
		})

		When("the destination-selectors contain a non-root directory", func() {
			It("errors", func() {
				mockPipelineDirectory := filepath.Join(getRootDirectory(), "destination-selectors-with-non-root-directory")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
				Expect(err).To(MatchError(ContainSubstring("invalid directory in destination-selectors.yaml: foo/bar, directory must be top-level")))
			})
		})

		When("the destination-selectors contain duplicate directories, one with a trailing slash and one without", func() {
			It("errors as they are treated as the same value", func() {
				mockPipelineDirectory := filepath.Join(getRootDirectory(), "destination-selectors-trailing-slash")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
				Expect(err).To(MatchError(ContainSubstring("duplicate entries in destination-selectors.yaml")))
			})
		})

		Context("with empty metadata directory", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "default", "resource-name", "resource", pipelineName)
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
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "", "resource-name", "resource", pipelineName)
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
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", "resource", pipelineName)
				Expect(err).ToNot(HaveOccurred())

				workResource = getWork(expectedNamespace, promiseName, resourceName, pipelineName)
			})

			It("does not append the default workload group to the work", func() {
				Expect(workResource.Spec.WorkloadGroups).To(HaveLen(1))

				paths := []string{}
				for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
					paths = append(paths, workload.Filepath)
				}
				Expect(paths).To(ConsistOf("baz/baz-namespace-resource-request.yaml"))
				for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
					fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
					Expect(err).NotTo(HaveOccurred())
					Expect(workload.Content).To(Equal(string(fileContent)))
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

		Context("complete set of inputs for a Promise", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "complete-for-promise"), "promise-name", "", "resource-name", "promise", pipelineName)
				Expect(err).NotTo(HaveOccurred())
			})

			It("has a correctly configured Work resource", func() {
				expectedNamespace = "kratix-platform-system"
				workResource := getWork(expectedNamespace, promiseName, "", pipelineName)

				Expect(workResource.Spec.Replicas).To(Equal(-1))
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
})

func getRootDirectory() string {
	d, _ := filepath.Abs("samples/")
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
	err = k8sClient.List(context.Background(), &works, &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, works.Items).To(HaveLen(1))

	return works.Items[0]
}
