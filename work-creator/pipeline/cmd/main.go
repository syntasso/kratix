package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/pipeline"
	"github.com/syntasso/kratix/work-creator/pipeline/lib"
	"github.com/syntasso/kratix/work-creator/pipeline/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "pipeline-adapter",
		Short: "Kratix pipeline adapter with unified commands",
	}

	// Add subcommands
	rootCmd.AddCommand(workCreatorCmd())
	rootCmd.AddCommand(updateStatusCmd())
	rootCmd.AddCommand(readerCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

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

			workCreator := pipeline.WorkCreator{
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

	cmd.MarkFlagRequired("input-directory")
	cmd.MarkFlagRequired("promise-name")
	cmd.MarkFlagRequired("pipeline-name")

	return cmd
}

func updateStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update-status",
		Short: "Update status of Kubernetes resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return runUpdateStatus(ctx)
		},
	}

	return cmd
}

func readerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reader",
		Short: "Read and process Kubernetes resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			r := lib.Reader{
				Out: os.Stdout,
			}
			return r.Run(ctx)
		},
	}

	return cmd
}

func runUpdateStatus(ctx context.Context) error {
	workspaceDir := "/work-creator-files"
	statusFile := filepath.Join(workspaceDir, "metadata", "status.yaml")

	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	existingObj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %w", err)
	}

	existingStatus := map[string]any{}
	if existingObj.Object["status"] != nil {
		existingStatus = existingObj.Object["status"].(map[string]any)
	}

	// Load incoming status.yaml if exists
	incomingStatus, err := readStatusFile(statusFile)
	if err != nil {
		return fmt.Errorf("failed to load incoming status: %w", err)
	}

	mergedStatus := lib.MergeStatuses(existingStatus, incomingStatus)
	if params.IsLastPipeline {
		mergedStatus = lib.MarkAsCompleted(mergedStatus)
	}

	// Apply merged status to the existing object
	existingObj.Object["status"] = mergedStatus

	// Update the object's status
	if _, err = objectClient.UpdateStatus(ctx, existingObj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

func readStatusFile(statusFile string) (map[string]any, error) {
	incomingStatus := map[string]any{}
	if _, err := os.Stat(statusFile); err == nil {
		incomingStatusBytes, err := os.ReadFile(statusFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read status file: %w", err)
		}
		if err := yaml.Unmarshal(incomingStatusBytes, &incomingStatus); err != nil {
			return nil, fmt.Errorf("failed to unmarshal incoming status: %w", err)
		}
	}
	return incomingStatus, nil
}

func getClient() (client.Client, error) {
	config := ctrl.GetConfigOrDie()
	return client.New(config, client.Options{Scheme: scheme.Scheme})
}
