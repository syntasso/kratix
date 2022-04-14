package controllers

import (
	"bytes"
	"context"

	"github.com/go-logr/logr"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type BucketWriter struct {
	Log logr.Logger
}

func (b *BucketWriter) WriteObject(bucketName string, objectName string, toWrite []byte) error {
	if len(toWrite) == 0 {
		b.Log.Info("Empty byte[]. Nothing to write to Minio for " + objectName)
		return nil
	}

	ctx := context.Background()
	endpoint := "minio.kratix-platform-system.svc.cluster.local"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})

	if err != nil {
		b.Log.Error(err, "Error initalising Minio client")
		return err
	}

	location := "local-minio"

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
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
	_, err = minioClient.PutObject(ctx, bucketName, objectName, reader, reader.Size(), minio.PutObjectOptions{ContentType: contentType})
	b.Log.Info("Minio object " + objectName + " written to " + bucketName)
	if err != nil {
		b.Log.Error(err, "Minio Error")
		return err
	}

	return nil
}
