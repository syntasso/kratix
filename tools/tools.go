//go:build ignore

package tools

/**
 * To develop and test our code, we need a few go tools like the ginkgo test runner. We
 * could use the binaries directly, but that depends on 1) having the binary installed on
 * everyone's machine, and 2) everyone is using the same versions of the tool. We can
 * solve both problems by using `go run` instead.
 *
 * To `go run`, the go.mod needs to include the dependencies needed to execute the tools.
 * However, since those tools are not imported anywhere, they won't be
 * persistet in our go.mod.
 *
 * One solution is to create a file (this one) that explicitly import the tools. To
 * ensure that the transient dependencies have no impact on the size of the compiled
 * binary, we use the go:build constraint on line 1.
 *
 * See https://pkg.go.dev/cmd/go#hdr-Build_constraints for more details
 */
import (
	// Counterfeiter is used to generate mocks
	_ "github.com/maxbrunsfeld/counterfeiter/v6"
	// Ginkgo is what we use to run our tests
	_ "github.com/onsi/ginkgo/v2/ginkgo"
)
