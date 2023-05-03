package writers

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

type S3Writer struct {
	Log        logr.Logger
	RepoClient *minio.Client
	BucketName string
}

func newS3Writer(logger logr.Logger, s3Spec *platformv1alpha1.S3Spec, creds map[string][]byte) (StateStoreWriter, error) {
	endpoint := s3Spec.Endpoint
	accessKeyID := string(creds["accessKeyID"])
	secretAccessKey := string(creds["secretAccessKey"])
	useSSL := s3Spec.UseSSL

	if accessKeyID == "" || secretAccessKey == "" {
		logger.Info("S3 credentials incomplete: accessKeyID or secretAccessKey are empty; will try unauthenticated access")
	}

	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})

	if err != nil {
		logger.Error(err, "Error initalising Minio client")
		return nil, err
	}

	return &S3Writer{
		Log:        logger,
		RepoClient: minioClient,
		BucketName: s3Spec.BucketName,
	}, nil
}

func (b *S3Writer) WriteObject(path string, objectName string, toWrite []byte) error {
	logger := b.Log.WithValues(
		"bucketName", b.BucketName,
		"objectPath", path,
		"objectName", objectName,
	)

	objectFullPath := filepath.Join(path, objectName)
	if len(toWrite) == 0 {
		logger.Info("Empty byte[]. Nothing to write to bucket")
		return nil
	}

	ctx := context.Background()

	// Check to see if we already own this bucket (which happens if you run this twice)
	exists, errBucketExists := b.RepoClient.BucketExists(ctx, b.BucketName)
	if errBucketExists != nil {
		logger.Error(errBucketExists, "Could not verify bucket existence with provider")
	} else if !exists {
		logger.Info("Bucket provided does not exist (or the provided keys don't have permissions)")
	}

	contentType := "text/x-yaml"
	reader := bytes.NewReader(toWrite)

	objStat, err := b.RepoClient.StatObject(ctx, b.BucketName, objectFullPath, minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			logger.Info("Object does not exist yet")
		} else {
			logger.Error(err, "Error fetching object")
			return err
		}
	} else {
		contentMd5 := fmt.Sprintf("%x", md5.Sum(toWrite))
		if objStat.ETag == contentMd5 {
			logger.Info("Content has not changed, will not re-write to bucket")
			return nil
		}
	}

	logger.Info("Writing object to bucket")
	_, err = b.RepoClient.PutObject(ctx, b.BucketName, objectFullPath, reader, reader.Size(), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		logger.Error(err, "Error writing object to bucket")
		return err
	}
	logger.Info("Object written to bucket")

	return nil
}

func (b *S3Writer) RemoveObject(path string, objectName string) error {
	logger := b.Log.WithValues(
		"bucketName", b.BucketName,
		"objectPath", path,
		"objectName", objectName,
	)
	logger.Info("Removing objects from bucket")
	ctx := context.Background()

	err := b.RepoClient.RemoveObject(
		ctx,
		b.BucketName,
		filepath.Join(path, objectName),
		minio.RemoveObjectOptions{},
	)
	if err != nil {
		b.Log.Error(err, "could not delete object", "bucketName", b.BucketName, "objectName", objectName)
		return err
	}
	logger.Info("Objects removed")

	return nil
}
