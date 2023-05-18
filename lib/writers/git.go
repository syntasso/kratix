package writers

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
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

func NewGitWriter(logger logr.Logger, stateStoreSpec platformv1alpha1.GitStateStoreSpec, cluster platformv1alpha1.Cluster, creds map[string][]byte) (StateStoreWriter, error) {
	return &GitWriter{
		gitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
			Auth: &http.BasicAuth{
				Username: string(creds["username"]),
				Password: string(creds["password"]),
			},
		},
		author: gitAuthor{
			Name:  "Kratix",
			Email: "kratix@syntasso.io",
		},
		Log:  logger,
		path: filepath.Join(stateStoreSpec.Path, cluster.Spec.Path, cluster.Namespace, cluster.Name),
	}, nil
}

func (g *GitWriter) WriteObject(fileName string, toWrite []byte) error {
	log := g.Log.WithValues(
		"dir", g.path,
		"fileName", fileName,
		"branch", g.gitServer.Branch,
	)
	if len(toWrite) == 0 {
		log.Info("Empty byte[]. Nothing to write to Git")
		return nil
	}

	localTmpDir, err := createLocalDirectory()
	if err != nil {
		log.Error(err, "could not create temporary repository directory")
		return err
	}
	defer os.RemoveAll(filepath.Dir(localTmpDir))

	repo, err := g.cloneRepo(localTmpDir)
	if err != nil {
		log.Error(err, "could not clone repository")
		return err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		log.Error(err, "could not access repo worktree")
		return err
	}

	workTreeFilePath := filepath.Join(g.path, fileName)
	absoluteFilePath := filepath.Join(localTmpDir, workTreeFilePath)

	if os.MkdirAll(filepath.Dir(absoluteFilePath), 0700); err != nil {
		log.Error(err, "could not generate local directories")
		return err
	}

	if err := ioutil.WriteFile(absoluteFilePath, toWrite, 0644); err != nil {
		log.Error(err, "could not write to file")
		return err
	}

	if _, err := worktree.Add(workTreeFilePath); err != nil {
		log.Error(err, "could not add file to worktree")
		return err
	}

	if err := g.commitAndPush(repo, worktree, Add, workTreeFilePath, log); err != nil {
		return err
	}

	return nil
}

func (g *GitWriter) RemoveObject(fileName string) error {
	log := g.Log.WithValues("dir", g.path, "fileName", fileName)

	repoPath, err := createLocalDirectory()
	if err != nil {
		log.Error(err, "could not create temporary repository directory")
		return err
	}
	defer os.RemoveAll(filepath.Dir(repoPath))

	repo, err := g.cloneRepo(repoPath)
	if err != nil {
		log.Error(err, "could not clone repository")
		return err
	}

	worktree, err := repo.Worktree()
	if err != nil {
		log.Error(err, "could not access repo worktree")
		return err
	}

	objectFileName := filepath.Join(g.path, fileName)
	if _, err := worktree.Filesystem.Lstat(objectFileName); err == nil {
		if _, err := worktree.Remove(objectFileName); err != nil {
			log.Error(err, "could not remove file from worktree")
			return err
		}
		log.Info("successfully deleted file from worktree")
	} else {
		log.Info("file does not exist on worktree, nothing to delete")
		return nil
	}

	if err := g.commitAndPush(repo, worktree, Delete, objectFileName, log); err != nil {
		return err
	}
	return nil
}

func (g *GitWriter) push(repo *git.Repository, log logr.Logger) error {
	err := repo.Push(&git.PushOptions{
		RemoteName:      "origin",
		Auth:            g.gitServer.Auth,
		InsecureSkipTLS: true,
	})
	if err != nil {
		log.Error(err, "could not push to remote")
		return err
	}
	return nil
}

func (g *GitWriter) cloneRepo(localRepoFilePath string) (*git.Repository, error) {
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

func (g *GitWriter) commitAndPush(repo *git.Repository, worktree *git.Worktree, action, fileToAdd string, log logr.Logger) error {
	status, err := worktree.Status()
	if err != nil {
		log.Error(err, "could not get worktree status")
		return err
	}

	if status.IsClean() {
		log.Info("no changes to be committed")
		return nil
	}

	//should fileToAdd be here at all? is it valuable? specifically the fileToAdd parameter
	_, err = worktree.Commit(fmt.Sprintf("%s: %s", action, fileToAdd), &git.CommitOptions{
		Author: &object.Signature{
			Name:  g.author.Name,
			Email: g.author.Email,
			When:  time.Now(),
		},
	})
	if err != nil {
		log.Error(err, "could not commit file to worktree")
		return err
	}

	if err := g.push(repo, log); err != nil {
		return err
	}
	return nil
}

func createLocalDirectory() (string, error) {
	dir, err := ioutil.TempDir("", "kratix-repo")
	if err != nil {
		return "", err
	}

	return dir, nil
}
