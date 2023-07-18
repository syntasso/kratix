package integration_test

import (
	"context"
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
			var inputDirectory string

			BeforeEach(func() {
				inputDirectory = filepath.Join(getRootDirectory(), "complete")
				err := workCreator.Execute(inputDirectory, getWorkResourceIdentifer(), "default")
				Expect(err).ToNot(HaveOccurred())

				workResource = getCreatedWorkResource()
			})

			It("has a correctly configured Work resource", func() {
				Expect(workResource.GetName()).To(Equal(getWorkResourceIdentifer()))
				Expect(workResource.Spec.Scheduling).To(Equal(
					v1alpha1.WorkScheduling{
						Promise: []v1alpha1.SchedulingConfig{
							{
								Target: v1alpha1.Target{
									MatchLabels: map[string]string{"environment": "dev"},
								},
							},
						},
						Resource: []v1alpha1.SchedulingConfig{
							{
								Target: v1alpha1.Target{
									MatchLabels: map[string]string{"environment": "production", "region": "europe"},
								},
							},
						},
					}))
			})

			Describe("the Work resource manifests list", func() {
				It("has three items", func() {
					expectedManifestsCount := 3 // This is the number of valid yaml resources defined in the input directory
					Expect(workResource.Spec.Workload.Manifests).To(HaveLen(expectedManifestsCount))
				})

				for _, expectedManifest := range getExpectedManifests(inputDirectory) {
					It("contains the expected resource with name: "+expectedManifest.GetName(), func() {
						actualManifests := workResource.Spec.Workload.Manifests
						Expect(actualManifests).To(ContainManifest(expectedManifest))
					})
				}
			})
		})

		Context("with empty metadata directory", func() {
			BeforeEach(func() {
				err := workCreator.Execute(filepath.Join(getRootDirectory(), "empty-metadata"), getWorkResourceIdentifer(), "default")
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not try to apply the metadata/scheduling.yaml when its not present", func() {
				workResource := getCreatedWorkResource()
				Expect(workResource.GetName()).To(Equal(getWorkResourceIdentifer()))
				Expect(workResource.Spec.Scheduling).To(Equal(
					v1alpha1.WorkScheduling{
						Promise: []v1alpha1.SchedulingConfig{
							{
								Target: v1alpha1.Target{
									MatchLabels: map[string]string{"environment": "dev"},
								},
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
	return "promise-targetnamespace-mydatabase"
}
