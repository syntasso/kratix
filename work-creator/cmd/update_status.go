package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
)

func updateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update-status",
		Short: "Update status of Kubernetes resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return runUpdateStatus(ctx)
		},
	}
}

func runUpdateStatus(ctx context.Context) error {
	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	return lib.UpdateStatus(ctx, "/work-creator-files/metadata", params, objectClient)
}
