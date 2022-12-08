package writers

import (
	"errors"

	"github.com/go-logr/logr"
)

type BucketWriter interface {
	WriteObject(bucketName string, objectName string, toWrite []byte) error
	RemoveObject(bucketName string, objectName string) error
}

const (
	S3  string = "s3"
	Git string = "git"
)

func NewBucketWriter(logger logr.Logger, repositoryType string) (BucketWriter, error) {
	switch repositoryType {
	case S3:
		return newMinIOBucketWriter(logger)
	case Git:
		return newGitBucketWriter(logger)
	default:
		return nil, errors.New("unknown repository type " + repositoryType)
	}
}
