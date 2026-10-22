package delivery_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDelivery(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Delivery Suite")
}
