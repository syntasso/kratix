package writers

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

import (
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
)

//counterfeiter:generate . StateStoreWriter
type StateStoreWriter interface {
	UpdateFiles(subDir string, workPlacementName string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error)
	ReadFile(filename string) ([]byte, error)
}

var ErrFileNotFound = fmt.Errorf("file not found")
