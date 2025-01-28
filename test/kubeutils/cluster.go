package kubeutils

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
)

type Cluster struct {
	Context string
	Name    string
}

var timeout, interval time.Duration

func SetTimeoutAndInterval(t, i time.Duration) {
	timeout = t
	interval = i
}

func (c Cluster) Kubectl(args ...string) string {
	args = append(args, "--context="+c.Context)

	command := exec.Command("kubectl", args...)
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)

	fmt.Fprintf(GinkgoWriter, "Running: kubectl %s\n", strings.Join(args, " "))

	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
	return string(session.Out.Contents())
}
