package writers

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/syntasso/kratix/api/v1alpha1"
)

const (
	AuthMethodIAM       = "IAM"
	AuthMethodAccessKey = "accessKey"
)

// S3Writer is a writer for S3.
type S3Writer struct {
	Log        logr.Logger
	RepoClient *minio.Client
	BucketName string
	Path       string
}

// NewS3Writer creates a new S3 writer.
func NewS3Writer(
	logger logr.Logger,
	stateStoreSpec v1alpha1.BucketStateStoreSpec,
	destinationPath string,
	creds map[string][]byte,
) (StateStoreWriter, error) {
	endpoint := stateStoreSpec.Endpoint

	opts := &minio.Options{
		Secure: !stateStoreSpec.Insecure,
	}

	logger.Info(
		"setting up s3 client",
		"authMethod", stateStoreSpec.AuthMethod,
		"endpoint", endpoint,
		"insecure", stateStoreSpec.Insecure,
	)

	switch stateStoreSpec.AuthMethod {
	case AuthMethodIAM:
		opts.Creds = credentials.NewIAM("")

	case "", AuthMethodAccessKey: // used to be optional so lets handle empty as the default
		if creds == nil {
			return nil, errors.New("secret not provided")
		}
		accessKeyID, ok := creds["accessKeyID"]
		if !ok {
			return nil, errors.New("secret is missing key: accessKeyID")
		}

		secretAccessKey, ok := creds["secretAccessKey"]
		if !ok {
			return nil, errors.New("secret is missing key: secretAccessKey")
		}
		opts.Creds = credentials.NewStaticV4(string(accessKeyID), string(secretAccessKey), "")

	default:
		return nil, fmt.Errorf("unknown authMethod: %s", stateStoreSpec.AuthMethod)
	}

	minioClient, err := minio.New(endpoint, opts)

	if err != nil {
		logger.Error(err, "Error initialising Minio client")
		err = fmt.Errorf("error initialising S3 client: %w", err)
		return nil, err
	}

	return &S3Writer{
		Log:        logger,
		RepoClient: minioClient,
		BucketName: stateStoreSpec.BucketName,
		Path:       filepath.Join(stateStoreSpec.Path, destinationPath),
	}, nil
}

func (b *S3Writer) ReadFile(filename string) ([]byte, error) {
	_, err := b.RepoClient.StatObject(context.Background(), b.BucketName, filepath.Join(b.Path, filename), minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrFileNotFound
		}
		return nil, err
	}

	obj, err := b.RepoClient.GetObject(context.Background(), b.BucketName, filepath.Join(b.Path, filename), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close() //nolint:errcheck

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(obj)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil

}

func (b *S3Writer) UpdateFiles(subDir string, _ string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	return b.update(subDir, workloadsToCreate, workloadsToDelete)
}

func (b *S3Writer) update(subDir string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	ctx := context.Background()
	logger := b.Log.WithValues("bucketName", b.BucketName, "path", b.Path)
	objectsToDeleteMap := map[string]minio.ObjectInfo{}

	//Get a list of all the old workload files, we delete any that aren't part of the new workload at the end of this function.
	if subDir != "" {
		var err error
		objectsToDeleteMap, err = b.getObjectsInDir(ctx, subDir, logger)
		if err != nil {
			return "", err
		}
	} else {
		for _, work := range workloadsToDelete {
			objStat, err := b.RepoClient.StatObject(ctx, b.BucketName, filepath.Join(b.Path, work), minio.GetObjectOptions{})
			if err != nil {
				if minio.ToErrorResponse(err).Code != "NoSuchKey" {
					logger.Error(err, "Error fetching object")
					return "", err
				}
				logger.Info("Object does not exist yet")
				continue
			}
			objectsToDeleteMap[objStat.Key] = objStat
		}
	}

	exists, errBucketExists := b.RepoClient.BucketExists(ctx, b.BucketName)
	if errBucketExists != nil {
		logger.Error(errBucketExists, "Could not verify bucket existence with provider")
	} else if !exists {
		logger.Info("Bucket provided does not exist (or the provided keys don't have permissions)")
	}

	var versionID string

	for _, work := range workloadsToCreate {
		objectFullPath := filepath.Join(b.Path, subDir, work.Filepath)
		delete(objectsToDeleteMap, objectFullPath)
		log := logger.WithValues("objectName", objectFullPath)

		reader := bytes.NewReader([]byte(work.Content))
		objStat, err := b.RepoClient.StatObject(ctx, b.BucketName, objectFullPath, minio.GetObjectOptions{})
		if err != nil {
			if minio.ToErrorResponse(err).Code != "NoSuchKey" {
				log.Error(err, "Error fetching object")
				return "", err
			}
			log.Info("Object does not exist yet")
		} else {
			contentMd5 := fmt.Sprintf("%x", md5.Sum([]byte(work.Content)))
			if objStat.ETag == contentMd5 {
				log.Info("Content has not changed, will not re-write to bucket")
				continue
			}
		}

		log.Info("Writing object to bucket")
		uploadInfo, err := b.RepoClient.PutObject(ctx, b.BucketName, objectFullPath, reader, reader.Size(), minio.PutObjectOptions{})
		if err != nil {
			log.Error(err, "Error writing object to bucket")
			return "", err
		}

		versionID = fmt.Sprintf("%x", sha256.Sum256([]byte(versionID+uploadInfo.VersionID)))
		log.Info("Object written to bucket")
	}

	return versionID, b.deleteObjects(ctx, objectsToDeleteMap, logger)
}

func (b *S3Writer) deleteObjects(ctx context.Context, oldObjectsToDelete map[string]minio.ObjectInfo, logger logr.Logger) error {
	var errCount int

	for objectName, objectInfo := range oldObjectsToDelete {
		err := b.RepoClient.RemoveObject(ctx, b.BucketName, objectInfo.Key, minio.RemoveObjectOptions{})
		if err != nil {
			logger.Error(err, "Failed to remove object", "objectName", objectName)
			errCount++
		}
	}

	if errCount != 0 {
		return fmt.Errorf("failed to delete %d objects", errCount)
	}

	return nil
}

func (b *S3Writer) getObjectsInDir(ctx context.Context, dir string, logger logr.Logger) (map[string]minio.ObjectInfo, error) {
	pathsToDelete := map[string]minio.ObjectInfo{}
	if !strings.HasSuffix(dir, "/") {
		dir = dir + "/"
	}
	objectCh := b.RepoClient.ListObjects(ctx, b.BucketName, minio.ListObjectsOptions{Prefix: filepath.Join(b.Path, dir), Recursive: true})
	for object := range objectCh {
		if object.Err != nil {
			logger.Error(object.Err, "Listing objects", "dir", dir)
			return nil, object.Err
		}
		pathsToDelete[object.Key] = object
	}

	return pathsToDelete, nil
}

func (b *S3Writer) RemoveObject(objectName string) error {
	logger := b.Log.WithValues(
		"bucketName", b.BucketName,
		"path", b.Path,
		"objectName", objectName,
	)
	logger.Info("Removing objects from bucket")
	ctx := context.Background()

	if strings.HasSuffix(objectName, "/") {
		var paths []string
		//list files and delete all
		objectCh := b.RepoClient.ListObjects(ctx, b.BucketName, minio.ListObjectsOptions{Prefix: filepath.Join(b.Path, objectName), Recursive: true})
		for object := range objectCh {
			if object.Err != nil {
				logger.Error(object.Err, "Listing objects", "dir", objectName)
				return object.Err
			}

			err := b.RepoClient.RemoveObject(
				ctx,
				b.BucketName,
				object.Key,
				minio.RemoveObjectOptions{},
			)
			if err != nil {
				b.Log.Error(err, "could not delete object", "bucketName", b.BucketName, "dir", objectName, "path", object.Key)
				return err
			}
			paths = append(paths, object.Key)
		}

		logger.Info("Object removed", "paths", paths)
	} else {
		err := b.RepoClient.RemoveObject(
			ctx,
			b.BucketName,
			filepath.Join(b.Path, objectName),
			minio.RemoveObjectOptions{},
		)
		if err != nil {
			b.Log.Error(err, "could not delete object", "bucketName", b.BucketName, "objectName", objectName)
			return err
		}
		logger.Info("Objects removed")
	}

	return nil
}

// ValidatePermissions checks if the S3Writer has the necessary permissions to write to the bucket
// without actually writing meaningful data. It validates write permissions by initiating a multipart
// upload and then aborting it, which verifies permissions without leaving any data in the bucket.
func (b *S3Writer) ValidatePermissions() error {
	ctx := context.Background()
	logger := b.Log.WithValues("bucketName", b.BucketName, "path", b.Path)

	testObjectPath := filepath.Join(b.Path, ".kratix-write-permission-check")

	coreClient := minio.Core{Client: b.RepoClient}

	// Step 1: Initiate a new multipart upload
	uploadID, err := coreClient.NewMultipartUpload(ctx, b.BucketName, testObjectPath, minio.PutObjectOptions{})
	if err != nil {
		return fmt.Errorf("write permission validation failed: %w", err)
	}

	// Step 2: Abort the multipart upload to clean up
	err = coreClient.AbortMultipartUpload(ctx, b.BucketName, testObjectPath, uploadID)
	if err != nil {
		logger.Info("Write permissions validated but failed to abort multipart upload", "error", err)
	}

	logger.Info("Successfully validated bucket write permissions via multipart upload")
	return nil
}
