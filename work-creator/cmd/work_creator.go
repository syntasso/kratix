package cmd

import (
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

func workCreatorCmd() *cobra.Command {
	var inputDirectory string
	var promiseName string
	var pipelineName string
	var namespace string
	var resourceName string
	var workflowType string

	cmd := &cobra.Command{
		Use:   "work-creator",
		Short: "Create Work resources from pipeline output",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("executing work creator with flags:",
				"input-directory", inputDirectory,
				"promise-name", promiseName,
				"pipeline-name", pipelineName,
				"namespace", namespace,
				"resource-name", resourceName,
				"workflow-type", workflowType)

			if inputDirectory == "" {
				return fmt.Errorf("must provide --input-directory")
			}

			if promiseName == "" {
				return fmt.Errorf("must provide --promise-name")
			}

			if pipelineName == "" {
				return fmt.Errorf("must provide --pipeline-name")
			}

			prefix := os.Getenv("KRATIX_LOGGER_PREFIX")
			if prefix != "" {
				ctrl.Log = ctrl.Log.WithName(prefix)
			}

			//Teach our client to speak v1alpha1.Work
			err := v1alpha1.AddToScheme(scheme.Scheme)
			if err != nil {
				return fmt.Errorf("error adding v1alpha1 to scheme: %w", err)
			}

			k8sClient, err := getClient()
			if err != nil {
				return fmt.Errorf("error creating k8s client: %w", err)
			}

			workCreator := lib.WorkCreator{
				K8sClient: k8sClient,
			}

			err = workCreator.Execute(inputDirectory, promiseName, namespace, resourceName, workflowType, pipelineName)
			if err != nil {
				return fmt.Errorf("work creator execution failed: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&inputDirectory, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	cmd.Flags().StringVar(&promiseName, "promise-name", "", "Name of the promise")
	cmd.Flags().StringVar(&pipelineName, "pipeline-name", "", "Name of the Pipeline in the Workflow")
	cmd.Flags().StringVar(&namespace, "namespace", v1alpha1.SystemNamespace, "Namespace")
	cmd.Flags().StringVar(&resourceName, "resource-name", "", "Name of the resource")
	cmd.Flags().StringVar(&workflowType, "workflow-type", "resource", "Create a Work for Promise or Resource type scheduling")

	if err := cmd.MarkFlagRequired("input-directory"); err != nil {
		log.Fatalf("error marking input-directory as required: %s", err)
	}
	if err := cmd.MarkFlagRequired("promise-name"); err != nil {
		log.Fatalf("error marking promise-name as required: %s", err)
	}
	if err := cmd.MarkFlagRequired("pipeline-name"); err != nil {
		log.Fatalf("error marking pipeline-name as required: %s", err)
	}

	return cmd
}
