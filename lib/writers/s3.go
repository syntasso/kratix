package writers

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
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

type S3Writer struct {
	Log        logr.Logger
	RepoClient *minio.Client
	BucketName string
	path       string
}

func NewS3Writer(logger logr.Logger, stateStoreSpec v1alpha1.BucketStateStoreSpec, destination v1alpha1.Destination, creds map[string][]byte) (StateStoreWriter, error) {
	endpoint := stateStoreSpec.Endpoint

	opts := &minio.Options{
		Secure: !stateStoreSpec.Insecure,
	}

	logger.Info("setting up s3 client", "authMethod", stateStoreSpec.AuthMethod, "endpoint", endpoint, "insecure", stateStoreSpec.Insecure)
	switch stateStoreSpec.AuthMethod {
	case AuthMethodIAM:
		opts.Creds = credentials.NewIAM("")

	case "", AuthMethodAccessKey: //used to be optional so lets handle empty as the default
		if creds == nil {
			return nil, fmt.Errorf("secret not provided")
		}
		accessKeyID, ok := creds["accessKeyID"]
		if !ok {
			return nil, fmt.Errorf("missing key accessKeyID")
		}

		secretAccessKey, ok := creds["secretAccessKey"]
		if !ok {
			return nil, fmt.Errorf("missing key secretAccessKey")
		}
		opts.Creds = credentials.NewStaticV4(string(accessKeyID), string(secretAccessKey), "")

	default:
		return nil, fmt.Errorf("unknown authMethod %s", stateStoreSpec.AuthMethod)
	}

	minioClient, err := minio.New(endpoint, opts)

	if err != nil {
		logger.Error(err, "Error initialising Minio client")
		return nil, err
	}

	return &S3Writer{
		Log:        logger,
		RepoClient: minioClient,
		BucketName: stateStoreSpec.BucketName,
		path:       filepath.Join(stateStoreSpec.Path, destination.Spec.Path, destination.Name),
	}, nil
}

func (b *S3Writer) ReadFile(filename string) ([]byte, error) {
	_, err := b.RepoClient.StatObject(context.Background(), b.BucketName, filepath.Join(b.path, filename), minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, FileNotFound
		}
		return nil, err
	}

	obj, err := b.RepoClient.GetObject(context.Background(), b.BucketName, filepath.Join(b.path, filename), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	buf := new(bytes.Buffer)
	buf.ReadFrom(obj)

	return buf.Bytes(), nil

}

func (b *S3Writer) UpdateFiles(subDir string, _ string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	return b.update(subDir, workloadsToCreate, workloadsToDelete)
}

func (b *S3Writer) update(subDir string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	ctx := context.Background()
	logger := b.Log.WithValues("bucketName", b.BucketName, "path", b.path)
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
			objStat, err := b.RepoClient.StatObject(ctx, b.BucketName, filepath.Join(b.path, work), minio.GetObjectOptions{})
			if err != nil {
				if minio.ToErrorResponse(err).Code != "NoSuchKey" {
					logger.Error(err, "Error fetching object")
					return "", err
				}
				logger.Info("Object does not exist yet")
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
		objectFullPath := filepath.Join(b.path, subDir, work.Filepath)
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
	objectsCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(objectsCh)
		for _, objectInfo := range oldObjectsToDelete {
			objectsCh <- objectInfo
		}
	}()

	errorCh := b.RepoClient.RemoveObjects(ctx, b.BucketName, objectsCh, minio.RemoveObjectsOptions{})

	// Print errCount received from RemoveObjects API
	var errCount int
	for e := range errorCh {
		logger.Error(e.Err, "Failed to remove object", "objectName", e.ObjectName)
		errCount++
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
	objectCh := b.RepoClient.ListObjects(ctx, b.BucketName, minio.ListObjectsOptions{Prefix: filepath.Join(b.path, dir), Recursive: true})
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
		"path", b.path,
		"objectName", objectName,
	)
	logger.Info("Removing objects from bucket")
	ctx := context.Background()

	if strings.HasSuffix(objectName, "/") {
		var paths []string
		//list files and delete all
		objectCh := b.RepoClient.ListObjects(ctx, b.BucketName, minio.ListObjectsOptions{Prefix: filepath.Join(b.path, objectName), Recursive: true})
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
			filepath.Join(b.path, objectName),
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
