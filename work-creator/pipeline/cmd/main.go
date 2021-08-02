package main

import (
	"flag"
	"fmt"
	"os"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	var inputDirectoy string
	var identifier string
	flag.StringVar(&inputDirectoy, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	flag.StringVar(&identifier, "identifier", "", "Unique name of the Work resource to be created")
	flag.Parse()

	if identifier == "" {
		fmt.Println("Must provide -identifier")
		os.Exit(1)
	}

	if inputDirectoy == "" {
		fmt.Println("Must provide -input-directory")
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
	err = workCreator.Execute(inputDirectoy, identifier)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func getClient() (client.Client, error) {
	config := ctrl.GetConfigOrDie()
	return client.New(config, client.Options{Scheme: scheme.Scheme})
}
