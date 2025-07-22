package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "pipeline-adapter",
	Short: "Kratix pipeline adapter with unified commands",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(workCreatorCmd())
	rootCmd.AddCommand(updateStatusCmd())
	rootCmd.AddCommand(readerCmd())
}
