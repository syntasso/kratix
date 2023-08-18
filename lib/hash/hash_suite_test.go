package hash_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestHash(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Hash Suite")
}
