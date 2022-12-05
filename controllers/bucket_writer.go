package controllers

import (
	"bytes"
	"context"
	"fmt"

	"github.com/go-logr/logr"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIOWriter struct {
	Log        logr.Logger
	RepoClient *minio.Client
}

type BucketWriter interface {
	WriteObject(bucketName string, objectName string, toWrite []byte) error
	RemoveObject(bucketName string, objectName string) error
}

func NewBucketWriter(logger logr.Logger) (BucketWriter, error) {
	endpoint := "minio.kratix-platform-system.svc.cluster.local"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})

	if err != nil {
		logger.Error(err, "Error initalising Minio client")
		return nil, err
	}

	return &MinIOWriter{
		Log:        logger,
		RepoClient: minioClient,
	}, nil

}

func (b *MinIOWriter) WriteObject(bucketName string, objectName string, toWrite []byte) error {
	if len(toWrite) == 0 {
		b.Log.Info("Empty byte[]. Nothing to write to Minio for " + objectName)
		return nil
	}

	ctx := context.Background()

	err := b.RepoClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: "local-minio"})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := b.RepoClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			b.Log.Info("Minio Bucket " + bucketName + " already exists, will not recreate\n")
		} else {
			b.Log.Error(err, "Error connecting to Minio")
			return errBucketExists
		}
	} else {
		b.Log.Info("Successfully created Minio Bucket " + bucketName)
	}

	contentType := "text/x-yaml"
	reader := bytes.NewReader(toWrite)

	b.Log.Info("Creating Minio object " + objectName)
	_, err = b.RepoClient.PutObject(ctx, bucketName, objectName, reader, reader.Size(), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		b.Log.Error(err, "Minio Error")
		return err
	}
	b.Log.Info("Minio object " + objectName + " written to " + bucketName)

	return nil
}

func (b *MinIOWriter) RemoveObject(bucketName string, objectName string) error {
	ctx := context.Background()

	err := b.RepoClient.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		b.Log.Error(err, fmt.Sprintf("could not delete %s/%s", bucketName, objectName))
		return err
	}

	return nil
}
