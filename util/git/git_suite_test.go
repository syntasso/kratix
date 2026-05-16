package git_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGitLibrary(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Git Library Suite")
}
