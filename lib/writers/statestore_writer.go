package writers

import (
	"fmt"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

type StateStoreWriter interface {
	WriteObject(path string, objectName string, toWrite []byte) error
	RemoveObject(bucketName string, objectName string) error
}

const (
	S3  string = "s3"
	Git string = "git"
)

func NewStateStoreWriter(logger logr.Logger, stateStore *platformv1alpha1.StateStore) (StateStoreWriter, error) {
	switch stateStore.Protocol() {
	case platformv1alpha1.StateStoreS3:
		return newS3Writer(logger, stateStore.Spec.S3, stateStore.Credentials())
	case platformv1alpha1.StateStoreGit:
		return newGitWriter(logger)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", stateStore.Protocol())
	}
}
