package writers

import "github.com/syntasso/kratix/api/v1alpha1"

const (
	DeleteExistingContentsInDir   = true
	PreserveExistingContentsInDir = false
)

type StateStoreWriter interface {
	WriteDirWithObjects(deleteExistingContentsInDir bool, dir string, workloads ...v1alpha1.Workload) error
	RemoveObject(objectName string) error
}
