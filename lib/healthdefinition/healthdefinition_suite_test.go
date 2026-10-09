package healthdefinition_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHealthDefinition(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HealthDefinition Suite")
}
