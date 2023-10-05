package pipeline_test

import (
	"context"
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var _ = Describe("WorkCreator", func() {
	var (
		resourceWorkName = "promise-name-resource-name"
		promiseWorkName  = "promise-name"
	)

	Describe("#Execute", func() {
		var (
			workCreator       pipeline.WorkCreator
			expectedNamespace string
		)

		BeforeEach(func() {
			expectedNamespace = "default"

			workCreator = pipeline.WorkCreator{
				K8sClient: k8sClient,
			}

			k8sClient.Create(context.Background(), &v1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kratix-platform-system",
				},
			})
		})

		Context("complete set of inputs", func() {
			var workResource v1alpha1.Work
			var mockPipelineDirectory string

			BeforeEach(func() {
				mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name", false)
				Expect(err).ToNot(HaveOccurred())

				workResource = getWork(expectedNamespace, resourceWorkName)
			})

			It("has a correctly configured Work resource", func() {
				Expect(workResource.GetName()).To(Equal(resourceWorkName))
				Expect(workResource.Spec.DestinationSelectors).To(Equal(
					v1alpha1.WorkScheduling{
						Promise: []v1alpha1.Selector{
							{
								MatchLabels: map[string]string{"environment": "dev"},
							},
						},
						Resource: []v1alpha1.Selector{
							{
								MatchLabels: map[string]string{"environment": "production", "region": "europe"},
							},
						},
					}))
			})

			Describe("the Work resource workloads list", func() {
				FIt("has three files", func() {
					Expect(workResource.Spec.WorkloadGroups).To(HaveLen(2))
					Expect(workResource.Spec.WorkloadGroups[0].Workloads).To(HaveLen(5))

					paths := []string{}
					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						paths = append(paths, workload.Filepath)
					}

					//order of files isn't guranteed
					Expect(paths).To(ConsistOf("configmap.yaml",
						"foo/bar/namespace-resource-request.yaml", "foo/multi-resource-requests.yaml"))

					for _, workload := range workResource.Spec.WorkloadGroups[0].Workloads {
						fileContent, err := os.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						Expect(workload.Content).To(Equal(string(fileContent)))
					}
				})
			})
		})

		Context("with empty metadata directory", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "default", "resource-name", false)
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not try to apply the metadata/destination-selectors.yaml when its not present", func() {
				workResource := getWork(expectedNamespace, resourceWorkName)
				Expect(workResource.GetName()).To(Equal(resourceWorkName))
				Expect(workResource.Spec.DestinationSelectors).To(Equal(
					v1alpha1.WorkScheduling{
						Promise: []v1alpha1.Selector{
							{
								MatchLabels: map[string]string{"environment": "dev"},
							},
						},
					}))
			})
		})

		Context("with empty namespace string", func() {
			BeforeEach(func() {
				expectedNamespace = "kratix-platform-system"
				err = workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "", "resource-name", false)
				Expect(err).NotTo(HaveOccurred())
			})

			It("creates works with the namespace 'kratix-platform-system'", func() {
				getWork(expectedNamespace, resourceWorkName)
			})
		})

		Context("with add-promise-dependencies true", func() {
			BeforeEach(func() {
				expectedNamespace = "kratix-platform-system"
				err = workCreator.Execute(filepath.Join(getRootDirectory(), "promise"), "promise-name", "", "resource-name", true)
				Expect(err).NotTo(HaveOccurred())
			})

			It("adds the dependencies in the promise to the work", func() {
				workResource := getWork(expectedNamespace, promiseWorkName)

				Expect(workResource.Spec.WorkloadGroups).To(HaveLen(2))
				Expect(workResource.Spec.WorkloadGroups[0].Workloads).To(HaveLen(4))
				Expect(workResource.Spec.WorkloadGroups[0].Workloads).To(ContainElement(v1alpha1.Workload{
					Content:  "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: dep-namespace\n",
					Filepath: "static/dependencies.0.yaml",
				}))
				Expect(workResource.Spec.WorkloadGroups[1].Workloads).To(HaveLen(1))
				Expect(workResource.Spec.WorkloadGroups[1].Workloads).To(ContainElement(v1alpha1.Workload{
					Content:  "apiVersion: v1\nkind: Namespace\nmetadata:\n  annotations:\n    kratix.io/destination-selectors-override: |\n      matchLabels:\n        override: true\n  name: dep-with-override\n",
					Filepath: "static/dependencies.1.yaml",
				}))
			})
		})
	})
})

func getRootDirectory() string {
	d, _ := filepath.Abs("samples/")
	return d
}

// Returns a []unstructured.Unstructured created from all Yaml documents contained
// in all files located in rootDirectory
func getExpectedManifests(rootDirectory string) []unstructured.Unstructured {
	inputDirectory := filepath.Join(rootDirectory, "/input")
	files, _ := os.ReadDir(inputDirectory)
	ul := []unstructured.Unstructured{}

	for _, fileInfo := range files {
		fileName := filepath.Join(inputDirectory, fileInfo.Name())
		file, err := os.Open(fileName)
		Expect(err).ToNot(HaveOccurred())

		decoder := yaml.NewYAMLOrJSONDecoder(file, 2048)
		for {
			us := unstructured.Unstructured{}
			err = decoder.Decode(&us)
			if err == io.EOF {
				//We reached the end of the file, move on to looking for the resource
				break
			} else {
				Expect(err).To(BeNil())
				//append the first resource to the resource slice, and go back through the loop
				ul = append(ul, us)
			}
		}
	}

	return ul
}

func getWork(namespace, name string) v1alpha1.Work {
	expectedName := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	ExpectWithOffset(1, k8sClient).NotTo(BeNil())
	work := v1alpha1.Work{}
	err := k8sClient.Get(context.Background(), expectedName, &work)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return work
}
