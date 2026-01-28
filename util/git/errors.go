package git

import "errors"

type GitClientError error

var (
	ErrNothingToCommit GitClientError = errors.New("nothing to commit, working tree clean")
)
