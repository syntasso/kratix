package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	var inputDirectoy string
	var promiseName string
	var namespace string
	var resourceName string
	var addPromiseDependencies bool

	flag.StringVar(&inputDirectoy, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	flag.StringVar(&promiseName, "promise-name", "", "Name of the promise")
	flag.StringVar(&namespace, "namespace", v1alpha1.KratixSystemNamespace, "Namespace")
	flag.StringVar(&resourceName, "resource-name", "", "Name of the resource")
	flag.BoolVar(&addPromiseDependencies, "add-promise-dependencies", false, "Add the dependencies in /work-creator-files/promise/object.yaml to the work")
	flag.Parse()

	if inputDirectoy == "" {
		fmt.Println("Must provide -input-directory")
		os.Exit(1)
	}

	if promiseName == "" {
		fmt.Println("Must provide -promise-name")
		os.Exit(1)
	}

	if resourceName == "" {
		fmt.Println("Must provide -resource-name")
		os.Exit(1)
	}

	//Teach our client to speak platformv1alpha1.Work
	platformv1alpha1.AddToScheme(scheme.Scheme)

	k8sClient, err := getClient()
	if err != nil {
		fmt.Println("Error creating k8s client")
		os.Exit(1)
	}

	workCreator := pipeline.WorkCreator{
		K8sClient: k8sClient,
	}
	err = workCreator.Execute(inputDirectoy, promiseName, namespace, resourceName, addPromiseDependencies)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func getClient() (client.Client, error) {
	config := ctrl.GetConfigOrDie()
	return client.New(config, client.Options{Scheme: scheme.Scheme})
}
