package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	yamlsig "sigs.k8s.io/yaml"
)

func main() {
	var resourcesPath string
	var promisePath string
	flag.StringVar(&resourcesPath, "k8s-resources-directory", "", "Absolute Path of k8s resources to build clusterWorkerResources from")
	flag.StringVar(&promisePath, "promise", "", "Absolute path of Promise to insert clusterWorkerResources into")
	flag.Parse()

	if resourcesPath == "" {
		fmt.Println("Must provide -k8s-resources-directory")
		os.Exit(1)
	}

	if promisePath == "" {
		fmt.Println("Must provide -promise")
		os.Exit(1)
	}

	//Read Resoures
	files, err := ioutil.ReadDir(resourcesPath)
	if err != nil {
		fmt.Println("Error reading resourcesPath: " + resourcesPath)
		os.Exit(1)
	}

	resources := []platformv1alpha1.ClusterWorkerResource{}

	for _, fileInfo := range files {
		fileName := filepath.Join(resourcesPath, fileInfo.Name())

		file, _ := os.Open(fileName)

		decoder := yaml.NewYAMLOrJSONDecoder(file, 2048)
		for {
			us := &unstructured.Unstructured{}
			err := decoder.Decode(&us)

			if us == nil {
				continue
			} else if err == io.EOF {
				break
			} else {
				resources = append(resources, platformv1alpha1.ClusterWorkerResource{Unstructured: *us})
			}
		}
	}

	//Read Promise
	promiseFile, _ := os.ReadFile(promisePath)
	promise := platformv1alpha1.Promise{}
	err = yaml.Unmarshal(promiseFile, &promise)
	//If there's an error unmarshalling from the template, log to stderr so it doesn't silently go to the promise
	if err != nil {
		l := log.New(os.Stderr, "", 0)
		l.Println(err.Error())
		os.Exit(1)
	}
	promise.Spec.ClusterWorkerResources = resources

	//Write Promise (with clusterWorkerResources) to stdout
	bytes, _ := yamlsig.Marshal(promise)
	fmt.Println(string(bytes))
}
