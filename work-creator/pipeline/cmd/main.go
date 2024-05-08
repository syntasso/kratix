package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	var inputDirectory string
	var promiseName string
	var pipelineName string
	var namespace string
	var resourceName string
	var workflowType string

	flag.StringVar(&inputDirectory, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	flag.StringVar(&promiseName, "promise-name", "", "Name of the promise")
	flag.StringVar(&pipelineName, "pipeline-name", "", "Name of the Pipeline in the Workflow")
	flag.StringVar(&namespace, "namespace", v1alpha1.SystemNamespace, "Namespace")
	flag.StringVar(&resourceName, "resource-name", "", "Name of the resource")
	flag.StringVar(&workflowType, "workflow-type", "resource", "Create a Work for Promise or Resource type scheduling")
	flag.Parse()

	if inputDirectory == "" {
		fmt.Println("Must provide -input-directory")
		os.Exit(1)
	}

	if promiseName == "" {
		fmt.Println("Must provide -promise-name")
		os.Exit(1)
	}

	prefix := os.Getenv("KRATIX_LOGGER_PREFIX")
	if prefix != "" {
		ctrl.Log = ctrl.Log.WithName(prefix)
	}

<<<<<<< HEAD
	if pipelineName == "" {
		fmt.Println("Must provide -pipeline-name")
		os.Exit(1)
	}

=======
>>>>>>> Bhargav-InfraCloud-br_fail_promiserelease_if_error_installing_promise
	//Teach our client to speak v1alpha1.Work
	v1alpha1.AddToScheme(scheme.Scheme)

	k8sClient, err := getClient()
	if err != nil {
		fmt.Println("Error creating k8s client")
		os.Exit(1)
	}

	workCreator := pipeline.WorkCreator{
		K8sClient: k8sClient,
	}

	err = workCreator.Execute(inputDirectory, promiseName, namespace, resourceName, workflowType, pipelineName)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func getClient() (client.Client, error) {
	config := ctrl.GetConfigOrDie()
	return client.New(config, client.Options{Scheme: scheme.Scheme})
}
