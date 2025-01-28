package newsystem_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNewsystem(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Newsystem Suite")
}
