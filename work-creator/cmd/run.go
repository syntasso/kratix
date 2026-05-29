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
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

// runCmd executes the post-pipeline steps as a single unit: build the Work
// resource from /work-creator-files (work-creator logic), then update the
// requesting object's status from /work-creator-files/metadata/status.yaml
// (update-status logic).
func runCmd() *cobra.Command {
	var inputDirectory string
	var promiseName string
	var pipelineName string
	var namespace string
	var resourceName string
	var resourceNamespace string
	var workflowType string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run post-pipeline work-creation followed by status-update",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("executing pipeline-adapter run with flags:",
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

			if err := v1alpha1.AddToScheme(scheme.Scheme); err != nil {
				return fmt.Errorf("error adding v1alpha1 to scheme: %w", err)
			}

			k8sClient, err := getClient()
			if err != nil {
				return fmt.Errorf("error creating k8s client: %w", err)
			}

			workCreator := lib.WorkCreator{K8sClient: k8sClient}
			if err := workCreator.Execute(inputDirectory, promiseName, namespace, resourceName, resourceNamespace, workflowType, pipelineName); err != nil {
				return fmt.Errorf("work creator execution failed: %w", err)
			}

			ctx := context.Background()
			params := helpers.GetParametersFromEnv()

			dynClient, err := helpers.GetK8sClient()
			if err != nil {
				return fmt.Errorf("failed to create dynamic Kubernetes client: %w", err)
			}
			objectClient := dynClient.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

			if err := lib.UpdateStatus(ctx, "/work-creator-files/metadata", params, objectClient); err != nil {
				return fmt.Errorf("status update failed: %w", err)
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
