package fetchers_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFetchers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Fetchers Suite")
}
