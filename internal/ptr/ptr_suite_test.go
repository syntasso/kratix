package ptr_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPtr(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ptr Suite")
}
