package git

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Git Native Client Suite")
}
