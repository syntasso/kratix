package pipeline_test

import (
	"context"
	"encoding/base64"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var _ = Describe("WorkCreator", func() {

	When("WorkCreator Executes", func() {
		var workCreator pipeline.WorkCreator

		BeforeEach(func() {
			//don't run main
			workCreator = pipeline.WorkCreator{
				K8sClient: k8sClient,
			}

			// //to test main
			// mainPath, err := gexec.Build("github.com/syntasso/kratix/work-creator/pipeline/cmd")
			// Expect(err).NotTo(HaveOccurred())

			// cmd := exec.Command(mainPath)
			// cmd.Args =
			// _, err = gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
			// Expect(err).NotTo(HaveOccurred())

			//after
			//gexec.CleanupBuildArtifacts()

		})

		Context("complete set of inputs", func() {
			var workResource v1alpha1.Work
			var mockPipelineDirectory string

			BeforeEach(func() {
				mockPipelineDirectory = filepath.Join(getRootDirectory(), "complete")
				err := workCreator.Execute(mockPipelineDirectory, "promise-name", "default", "resource-name")
				Expect(err).ToNot(HaveOccurred())

				workResource = getCreatedWorkResource()
			})

			It("has a correctly configured Work resource", func() {
				Expect(workResource.GetName()).To(Equal(getWorkResourceIdentifer()))
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
				It("has two files", func() {
					Expect(workResource.Spec.Workloads).To(HaveLen(3))

					paths := []string{}
					for _, workload := range workResource.Spec.Workloads {
						paths = append(paths, workload.Filepath)
					}

					//order of files isn't guranteed
					Expect(paths).To(ConsistOf("configmap.yaml",
						"foo/bar/namespace-resource-request.yaml", "foo/multi-resource-requests.yaml"))

					for _, workload := range workResource.Spec.Workloads {
						fileContent, err := ioutil.ReadFile(filepath.Join(mockPipelineDirectory, "input", workload.Filepath))
						Expect(err).NotTo(HaveOccurred())
						contentDecoded, err := base64.StdEncoding.DecodeString(string(workload.Content))
						Expect(err).NotTo(HaveOccurred())
						Expect(contentDecoded).To(Equal(fileContent))
					}
				})
			})
		})

		Context("with empty metadata directory", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), "promise-name", "default", "resource-name")
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not try to apply the metadata/destination-selectors.yaml when its not present", func() {
				workResource := getCreatedWorkResource()
				Expect(workResource.GetName()).To(Equal(getWorkResourceIdentifer()))
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
	files, _ := ioutil.ReadDir(inputDirectory)
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

func getCreatedWorkResource() v1alpha1.Work {
	expectedName := types.NamespacedName{
		Name:      getWorkResourceIdentifer(),
		Namespace: "default",
	}
	Expect(k8sClient).ToNot(BeNil())
	work := v1alpha1.Work{}
	err := k8sClient.Get(context.Background(), expectedName, &work)
	Expect(err).ToNot(HaveOccurred())
	return work
}

// our test identifer
func getWorkResourceIdentifer() string {
	return "promise-name-default-resource-name"
}
