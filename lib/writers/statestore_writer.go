package writers

import (
	"fmt"
	"github.com/syntasso/kratix/api/v1alpha1"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 . StateStoreWriter
type StateStoreWriter interface {
	UpdateFiles(workPlacementName string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) error
	UpdateInDir(subDir, workPlacementName string, workloadsToCreate []v1alpha1.Workload) error
	ReadFile(filename string) ([]byte, error)
}

var FileNotFound = fmt.Errorf("file not found")
