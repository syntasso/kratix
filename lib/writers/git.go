package writers

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

type GitWriter struct {
	Log       logr.Logger
	gitServer gitServer
	author    gitAuthor
	path      string
}

type gitServer struct {
	URL    string
	Branch string
	Auth   *http.BasicAuth
}

type gitAuthor struct {
	Name  string
	Email string
}

const (
	Add    string = "Add"
	Delete string = "Delete"
)

func NewGitWriter(logger logr.Logger, stateStoreSpec platformv1alpha1.GitStateStoreSpec, destination platformv1alpha1.Destination, creds map[string][]byte) (StateStoreWriter, error) {
	username, ok := creds["username"]
	if !ok {
		return nil, fmt.Errorf("username not found in secret %s/%s", destination.Namespace, stateStoreSpec.SecretRef.Name)
	}

	password, ok := creds["password"]
	if !ok {
		return nil, fmt.Errorf("password not found in secret %s/%s", destination.Namespace, stateStoreSpec.SecretRef.Name)
	}

	return &GitWriter{
		gitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
			Auth: &http.BasicAuth{
				Username: string(username),
				Password: string(password),
			},
		},
		author: gitAuthor{
			Name:  "Kratix",
			Email: "kratix@syntasso.io",
		},
		Log:  logger,
		path: filepath.Join(stateStoreSpec.Path, destination.Spec.Path, destination.Namespace, destination.Name),
	}, nil
}

func (g *GitWriter) setupLocalDirectoryWithRepo(logger logr.Logger) (string, *git.Repository, *git.Worktree, error) {
	localTmpDir, err := createLocalDirectory(logger)
	if err != nil {
		logger.Error(err, "could not create temporary repository directory")
		return "", nil, nil, err
	}

	repo, err := g.cloneRepo(localTmpDir, logger)
	if err != nil {
		logger.Error(err, "could not clone repository")
		return "", nil, nil, err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		logger.Error(err, "could not access repo worktree")
		return "", nil, nil, err
	}
	return localTmpDir, repo, worktree, nil
}

func (g *GitWriter) WriteDirWithObjects(deleteExistingContentsInDir bool, subDir string, toWrite ...platformv1alpha1.WorkloadGroup) error {
	dirInGitRepo := filepath.Join(g.path, subDir)
	logger := g.Log.WithValues(
		"dir", dirInGitRepo,
		"branch", g.gitServer.Branch,
	)

	if len(toWrite) == 0 {
		logger.Info("Empty workloads. Nothing to write to Git")
		return nil
	}

	localTmpDir, repo, worktree, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(localTmpDir))

	if deleteExistingContentsInDir {
		logger.Info("checking if any existing directories needs to be deleted")
		if _, err := worktree.Filesystem.Lstat(dirInGitRepo); err == nil {
			logger.Info("deleting existing content")
			if _, err := worktree.Remove(dirInGitRepo); err != nil {
				logger.Error(err, "could not add directory deletion to worktree", "dir", dirInGitRepo)
				return err
			}
		}
	}

	var filesCommitted []string
	for _, item := range toWrite {
		//worker-cluster/resources/<rr-namespace>/<promise-name>/<rr-name>/foo/bar/baz.yaml
		worktreeFilePath := filepath.Join(dirInGitRepo, item.Filepath)
		logger := logger.WithValues(
			"filepath", worktreeFilePath,
		)

		///tmp/git-dir/worker-cluster/resources/<rr-namespace>/<promise-name>/<rr-name>/foo/bar/baz.yaml
		absoluteFilePath := filepath.Join(localTmpDir, worktreeFilePath)

		//We need to protect against paths containg `..`
		//filepath.Join expands any '../' in the path to the actual, e.g. /tmp/foo/../ resolves to /tmp/
		//To ensure they can't write to files on disk outside of the tmp git repostiroy we check the absolute path
		//returned by `filepath.Join` is still contained with the git repository:
		// Note: This means `../` can still be used, but only if the end result is still contained within the git repository
		if !strings.HasPrefix(absoluteFilePath, localTmpDir) {
			logger.Error(nil, "path of file to write is not located within the git repostiory", "absolutePath", absoluteFilePath, "tmpDir", localTmpDir)
			return nil //We don't want to retry as this isn't a recoverable error. Log error and return nil.
		}

		if os.MkdirAll(filepath.Dir(absoluteFilePath), 0700); err != nil {
			logger.Error(err, "could not generate local directories")
			return err
		}

		if err := os.WriteFile(absoluteFilePath, []byte(item.Content), 0644); err != nil {
			logger.Error(err, "could not write to file")
			return err
		}

		if _, err := worktree.Add(worktreeFilePath); err != nil {
			logger.Error(err, "could not add file to worktree")
			return err
		}
		filesCommitted = append(filesCommitted, worktreeFilePath)
	}

	return g.commitAndPush(repo, worktree, Add, filesCommitted, logger)
}

func (g *GitWriter) RemoveObject(filePath string) error {
	logger := g.Log.WithValues("dir", g.path, "filepath", filePath)

	localTmpDir, repo, worktree, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(localTmpDir))

	worktreeFilepath := filepath.Join(g.path, filePath)
	if _, err := worktree.Filesystem.Lstat(worktreeFilepath); err == nil {
		if _, err := worktree.Remove(worktreeFilepath); err != nil {
			logger.Error(err, "could not remove file from worktree")
			return err
		}
		logger.Info("successfully deleted file from worktree")
	} else {
		logger.Info("file does not exist on worktree, nothing to delete")
		return nil
	}

	if err := g.commitAndPush(repo, worktree, Delete, []string{worktreeFilepath}, logger); err != nil {
		return err
	}
	return nil
}

func (g *GitWriter) push(repo *git.Repository, logger logr.Logger) error {
	err := repo.Push(&git.PushOptions{
		RemoteName:      "origin",
		Auth:            g.gitServer.Auth,
		InsecureSkipTLS: true,
	})
	if err != nil {
		logger.Error(err, "could not push to remote")
		return err
	}
	return nil
}

func (g *GitWriter) cloneRepo(localRepoFilePath string, logger logr.Logger) (*git.Repository, error) {
	logger.Info("cloning repo")
	return git.PlainClone(localRepoFilePath, false, &git.CloneOptions{
		Auth:            g.gitServer.Auth,
		URL:             g.gitServer.URL,
		ReferenceName:   plumbing.NewBranchReferenceName(g.gitServer.Branch),
		SingleBranch:    true,
		Depth:           1,
		NoCheckout:      false,
		InsecureSkipTLS: true,
	})
}

func (g *GitWriter) commitAndPush(repo *git.Repository, worktree *git.Worktree, action string, filesToAdd []string, logger logr.Logger) error {
	status, err := worktree.Status()
	if err != nil {
		logger.Error(err, "could not get worktree status")
		return err
	}

	if status.IsClean() {
		logger.Info("no changes to be committed")
		return nil
	}

	//should fileToAdd be here at all? is it valuable? specifically the fileToAdd parameter
	logger.Info("commiting changes", "filesAdded", filesToAdd)
	_, err = worktree.Commit(fmt.Sprintf("%s: %v", action, filesToAdd), &git.CommitOptions{
		Author: &object.Signature{
			Name:  g.author.Name,
			Email: g.author.Email,
			When:  time.Now(),
		},
	})

	if err != nil {
		logger.Error(err, "could not commit file to worktree")
		return err
	}

	logger.Info("pushing changes")
	if err := g.push(repo, logger); err != nil {
		logger.Error(err, "could not push changes")
		return err
	}
	return nil
}

func createLocalDirectory(logger logr.Logger) (string, error) {
	logger.Info("creating local directory")
	dir, err := ioutil.TempDir("", "kratix-repo")
	if err != nil {
		return "", err
	}

	return dir, nil
}
