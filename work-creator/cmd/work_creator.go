package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/telemetry"
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
	var resourceNamespace string
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
				"resource-namespace", resourceNamespace,
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

			otelLogger := ctrl.Log.WithName("telemetry")
			if shutdown, err := telemetry.SetupTracerProvider(cmd.Context(), otelLogger, "kratix-work-creator", nil); err != nil {
				otelLogger.Error(err, "failed to configure OpenTelemetry tracing")
			} else {
				defer func() {
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := shutdown(shutdownCtx); err != nil {
						otelLogger.Error(err, "failed to shutdown OpenTelemetry tracing")
					}
				}()
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

			err = workCreator.Execute(inputDirectory, promiseName, namespace, resourceName, resourceNamespace, workflowType, pipelineName)
			if err != nil {
				return fmt.Errorf("work creator execution failed: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&inputDirectory, "input-directory", "", "Absolute path to directory containing yaml documents required to build Work")
	cmd.Flags().StringVar(&promiseName, "promise-name", "", "Name of the promise")
	cmd.Flags().StringVar(&pipelineName, "pipeline-name", "", "Name of the Pipeline in the Workflow")
	cmd.Flags().StringVar(&namespace, "namespace", v1alpha1.SystemNamespace, "Namespace of the workflow")
	cmd.Flags().StringVar(&resourceName, "resource-name", "", "Name of the resource")
	cmd.Flags().StringVar(&resourceNamespace, "resource-namespace", "", "Namespace of the resource")
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
