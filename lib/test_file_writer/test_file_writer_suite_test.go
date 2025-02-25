package utils_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTestFileWriter(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TestFileWriter Suite")
}
