package writers

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"

	"github.com/go-logr/logr"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIOWriter struct {
	Log        logr.Logger
	RepoClient *minio.Client
}

func newMinIOBucketWriter(logger logr.Logger) (BucketWriter, error) {
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
	logger := b.Log.WithValues(
		"bucketName", bucketName,
		"objectName", objectName,
	)

	if len(toWrite) == 0 {
		logger.Info("Empty byte[]. Nothing to write to Minio")
		return nil
	}

	ctx := context.Background()

	err := b.RepoClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: "local-minio"})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := b.RepoClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			logger.Info("Minio Bucket already exists, will not recreate")
		} else {
			logger.Error(err, "Error connecting to Minio")
			return err
		}
	} else {
		logger.Info("Successfully created Minio Bucket")
	}

	contentType := "text/x-yaml"
	reader := bytes.NewReader(toWrite)

	minioObjStat, err := b.RepoClient.StatObject(ctx, bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			logger.Info("Object does not exist yet")
		} else {
			logger.Error(err, "Error fetching object")
			return err
		}
	} else {
		contentMd5 := fmt.Sprintf("%x", md5.Sum(toWrite))
		if minioObjStat.ETag == contentMd5 {
			logger.Info("Content has not changed, will not re-write to bucket")
			return nil
		}
	}

	logger.Info("Creating Minio object")
	_, err = b.RepoClient.PutObject(ctx, bucketName, objectName, reader, reader.Size(), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		logger.Error(err, "Error writing object to bucket")
		return err
	}
	logger.Info("Minio object written to bucket", "bucketName", bucketName)

	return nil
}

func (b *MinIOWriter) RemoveObject(bucketName string, objectName string) error {
	ctx := context.Background()

	err := b.RepoClient.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		b.Log.Error(err, "could not delete object", "bucketName", bucketName, "objectName", objectName)
		return err
	}

	return nil
}
