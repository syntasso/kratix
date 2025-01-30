package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	yamlsig "sigs.k8s.io/yaml"
)

func main() {
	var resourcesPath string
	var promisePath string
	flag.StringVar(&resourcesPath, "resources-dir", "", "Absolute Path of the directory containing dependencies")
	flag.StringVar(&promisePath, "promise", "", "Absolute path of Promise to insert dependencies into")
	flag.Parse()

	if resourcesPath == "" {
		fmt.Println("Must provide -resources-dir")
		os.Exit(1)
	}

	if promisePath == "" {
		fmt.Println("Must provide -promise")
		os.Exit(1)
	}

	//Read Resources
	files, err := os.ReadDir(resourcesPath)
	if err != nil {
		fmt.Println("Error reading resourcesPath: " + resourcesPath)
		os.Exit(1)
	}

	resources := []v1alpha1.Dependency{}

	for _, fileInfo := range files {
		fileName := filepath.Join(resourcesPath, fileInfo.Name())

		file, _ := os.Open(fileName)

		decoder := yaml.NewYAMLOrJSONDecoder(file, 2048)
		for {
			us := &unstructured.Unstructured{}
			err := decoder.Decode(&us)

			if us == nil {
				continue
			} else if errors.Is(err, io.EOF) {
				break
			} else {
				if us.GetNamespace() == "" && us.GetKind() != "Namespace" {
					us.SetNamespace("default")
				}
				resources = append(resources, v1alpha1.Dependency{Unstructured: *us})
			}
		}
	}

	//Read Promise
	promiseFile, err := os.ReadFile(promisePath)
	if err != nil {
		log.Fatal(err)
	}
	promise := v1alpha1.Promise{}
	err = yaml.Unmarshal(promiseFile, &promise)
	//If there's an error unmarshalling from the template, log to stderr so it doesn't silently go to the promise
	if err != nil {
		l := log.New(os.Stderr, "", 0)
		l.Println(err.Error())
		os.Exit(1)
	}
	promise.Spec.Dependencies = resources

	//Write Promise (with dependencies) to stdout
	bytes, _ := yamlsig.Marshal(promise)
	fmt.Println(string(bytes))
}
