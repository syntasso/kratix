package cmd

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/work-creator/lib"
)

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
