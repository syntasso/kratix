package git

import "errors"

type GitClientError error

var (
	ErrNoFilesChanged  GitClientError = errors.New("no files changed")
	ErrNothingToCommit GitClientError = errors.New("nothing to commit, working tree clean")
)
