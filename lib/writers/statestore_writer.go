package writers

import platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"

const (
	DeleteExistingContentsInDir   = true
	PreserveExistingContentsInDir = false
)

type StateStoreWriter interface {
	WriteDirWithObjects(deleteExistingContentsInDir bool, dir string, workloads ...platformv1alpha1.WorkloadGroup) error
	RemoveObject(objectName string) error
}
