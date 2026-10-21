package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	var workloadsDirectory string
	var metadataDirectory string
	var promiseSchedulingDirectory string
	var promiseName string
	var pipelineName string
	var namespace string
	var resourceName string
	var resourceNamespace string
	var workflowType string
	var promiseVersion string

	cmd := &cobra.Command{
		Use:   "work-creator",
		Short: "Create Work resources from pipeline output",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("executing work creator with flags:",
				"input-directory", inputDirectory,
				"workloads-directory", workloadsDirectory,
				"metadata-directory", metadataDirectory,
				"promise-scheduling-directory", promiseSchedulingDirectory,
				"promise-name", promiseName,
				"pipeline-name", pipelineName,
				"namespace", namespace,
				"resource-name", resourceName,
				"resource-namespace", resourceNamespace,
				"workflow-type", workflowType,
				"promise-version", promiseVersion)

			if inputDirectory != "" {
				workloadsDirectory = filepath.Join(inputDirectory, "input")
				metadataDirectory = filepath.Join(inputDirectory, "metadata")
				promiseSchedulingDirectory = filepath.Join(inputDirectory, "kratix-system")
			}

			usingDirectoryFlags := inputDirectory == ""
			if usingDirectoryFlags && !cmd.Flags().Changed("workflow-type") {
				return fmt.Errorf("must provide --workflow-type when using --workloads-directory")
			}

			versioned := promiseVersion != "" && promiseVersion != v1alpha1.PlaceholderPromiseVersion
			if metadataDirectory == "" && workflowType == string(v1alpha1.WorkflowTypeResource) && versioned {
				return fmt.Errorf("must provide --metadata-directory when --workflow-type is resource and --promise-version is set")
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

			err = workCreator.Execute(lib.ExecuteOptions{
				WorkloadsDirectory:         workloadsDirectory,
				MetadataDirectory:          metadataDirectory,
				PromiseSchedulingDirectory: promiseSchedulingDirectory,
				PromiseName:                promiseName,
				Namespace:                  namespace,
				ResourceName:               resourceName,
				ResourceNamespace:          resourceNamespace,
				WorkflowType:               workflowType,
				PipelineName:               pipelineName,
				PromiseVersion:             promiseVersion,
			})
			if err != nil {
				return fmt.Errorf("work creator execution failed: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&inputDirectory, "input-directory", "", "Absolute path to a directory with input/, metadata/ and kratix-system/ folders")
	cmd.Flags().StringVar(&workloadsDirectory, "workloads-directory", "", "Absolute path to the directory with the documents to schedule")
	cmd.Flags().StringVar(&metadataDirectory, "metadata-directory", "", "Absolute path to the pipeline metadata directory (optional)")
	cmd.Flags().StringVar(&promiseSchedulingDirectory, "promise-scheduling-directory", "", "Absolute path to the directory with the promise-scheduling file (optional)")
	cmd.Flags().StringVar(&promiseName, "promise-name", "", "Name of the promise")
	cmd.Flags().StringVar(&pipelineName, "pipeline-name", "", "Name of the Pipeline in the Workflow")
	cmd.Flags().StringVar(&namespace, "namespace", v1alpha1.SystemNamespace, "Namespace of the workflow")
	cmd.Flags().StringVar(&resourceName, "resource-name", "", "Name of the resource")
	cmd.Flags().StringVar(&resourceNamespace, "resource-namespace", "", "Namespace of the resource")
	cmd.Flags().StringVar(&workflowType, "workflow-type", "resource", "Create a Work for Promise or Resource type scheduling")
	cmd.Flags().StringVar(&promiseVersion, "promise-version", "", "Version of the promise; HealthDefinitions in the output are stamped with it")

	if err := cmd.Flags().MarkDeprecated("input-directory", "use --workloads-directory, --metadata-directory and --promise-scheduling-directory instead"); err != nil {
		log.Fatalf("error marking input-directory as deprecated: %s", err)
	}
	cmd.MarkFlagsOneRequired("input-directory", "workloads-directory")
	cmd.MarkFlagsMutuallyExclusive("input-directory", "workloads-directory")
	cmd.MarkFlagsMutuallyExclusive("input-directory", "metadata-directory")
	cmd.MarkFlagsMutuallyExclusive("input-directory", "promise-scheduling-directory")
	if err := cmd.MarkFlagRequired("promise-name"); err != nil {
		log.Fatalf("error marking promise-name as required: %s", err)
	}
	if err := cmd.MarkFlagRequired("pipeline-name"); err != nil {
		log.Fatalf("error marking pipeline-name as required: %s", err)
	}

	return cmd
}
