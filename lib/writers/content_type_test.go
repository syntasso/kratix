package writers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/lib/writers"
)

var _ = Describe("ContentTypeFor", func() {
	DescribeTable("maps a workload path to the type stored with the object",
		func(path, expected string) {
			Expect(writers.ContentTypeFor(path)).To(Equal(expected))
		},
		Entry("yaml", "namespace.yaml", "application/yaml"),
		Entry("yml", "namespace.yml", "application/yaml"),
		Entry("json", "config.json", "application/json"),
		Entry("uppercase extension", "NAMESPACE.YAML", "application/yaml"),
		Entry("nested path", "dir/sub/health-record.yaml", "application/yaml"),
		Entry("unknown extension", "archive.tar.gz", "application/octet-stream"),
		Entry("no extension", "README", "application/octet-stream"),
		Entry("empty path", "", "application/octet-stream"),
	)
})
