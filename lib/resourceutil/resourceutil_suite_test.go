package resourceutil_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestResourceutil(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Resourceutil Suite")
}
