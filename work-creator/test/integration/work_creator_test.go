package integration_test

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var _ = Describe("WorkCreator", func() {

	var inputDirectory = getInputDirectory()

	When("WorkCreator Executes", func() {
		var workResource platformv1alpha1.Work

		BeforeEach(func() {
			//don't run main
			workCreator := pipeline.WorkCreator{
				K8sClient: k8sClient,
			}

			err := workCreator.Execute(inputDirectory, getWorkResourceIdentifer())
			Expect(err).ToNot(HaveOccurred())

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

		It("has a correctly configured Work resource", func() {
			workResource = getCreatedWorkResource()
			Expect(workResource.GetName()).To(Equal(getWorkResourceIdentifer()))
			//Expect(workResource.Kind).To(Equal("Work"))
			//Expect(workResource.APIVersion).To(Equal("platform.kratix.syntasso.io/v1alpha1"))
		})

		Describe("the Work resource manifests list", func() {
			It("has three items", func() {
				expectedManifestsCount := len(getExpectedManifests())
				Expect(workResource.Spec.Workload.Manifests).To(HaveLen(expectedManifestsCount))
			})

			for _, expectedManifest := range getExpectedManifests() {
				It("contains the expected resource with name: "+expectedManifest.GetName(), func() {
					actualManifests := workResource.Spec.Workload.Manifests
					Expect(actualManifests).To(ContainManifest(expectedManifest))
				})
			}
		})
	})
})

func getInputDirectory() string {
	d, _ := filepath.Abs("samples")
	return d
}

// Returns a []unstructured.Unstructured created from all Yaml documents contained
// in all files located in inputDirectory
func getExpectedManifests() []unstructured.Unstructured {
	inputDirectory := getInputDirectory()
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
				//append the first resource to the resource slice, and go back through the loop
				ul = append(ul, us)
			}
		}
	}

	return ul
}

func getCreatedWorkResource() platformv1alpha1.Work {
	expectedName := types.NamespacedName{
		Name:      getWorkResourceIdentifer(),
		Namespace: "default",
	}
	Expect(k8sClient).ToNot(BeNil())
	work := platformv1alpha1.Work{}
	err := k8sClient.Get(context.Background(), expectedName, &work)
	Expect(err).ToNot(HaveOccurred())
	return work
}

//our test identifer
func getWorkResourceIdentifer() string {
	return "promise-targetnamespace-mydatabase"
}
