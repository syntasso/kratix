package kubeutils

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"sigs.k8s.io/yaml"
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

func (c Cluster) KubectlG(g Gomega, args ...string) string {
	return c.kubectlInternalG(g, true, args...)
}

func (c Cluster) Kubectl(args ...string) string {
	return c.kubectlInternalG(Default, true, args...)
}

func (c Cluster) KubectlAllowFail(args ...string) string {
	return c.kubectlInternalG(Default, false, args...)
}

// transientKubectlErrors match apiserver blips (overloaded kind clusters, brief
// unavailability) that should be retried rather than failing the calling spec.
var transientKubectlErrors = []string{
	"TLS handshake timeout",
	"connection refused",
	"connection reset by peer",
	"unexpected EOF",
	"etcdserver: request timed out",
	"the server is currently unable to handle the request",
	"error dialing backend", //nolint:misspell // the apiserver emits the US spelling
}

func isTransientKubectlError(stderr string) bool {
	for _, pattern := range transientKubectlErrors {
		if strings.Contains(stderr, pattern) {
			return true
		}
	}
	return false
}

func (c Cluster) kubectlInternalG(g Gomega, checkExitCode bool, args ...string) string {
	GinkgoHelper()
	args = append(args, "--context="+c.Context)

	var session *gexec.Session
	// Retry transient apiserver failures so a blip fails the current poll (or
	// nothing at all) instead of the whole spec.
	const attempts = 3
	for attempt := 1; ; attempt++ {
		command := exec.Command("kubectl", args...)
		var err error
		session, err = gexec.Start(command, GinkgoWriter, GinkgoWriter)

		fmt.Fprintf(GinkgoWriter, "Running: kubectl %s\n", strings.Join(args, " "))

		g.ExpectWithOffset(2, err).ShouldNot(HaveOccurred())

		g.EventuallyWithOffset(2, session, timeout, interval).Should(gexec.Exit())

		if session.ExitCode() == 0 {
			break
		}

		fmt.Fprintf(GinkgoWriter, "kubectl %s exited %d\nstdout: %s\nstderr: %s\n",
			strings.Join(args, " "), session.ExitCode(),
			session.Out.Contents(), session.Err.Contents())

		if attempt >= attempts || !isTransientKubectlError(string(session.Err.Contents())) {
			break
		}
		fmt.Fprintf(GinkgoWriter, "transient apiserver error, retrying (%d/%d)\n", attempt, attempts)
		time.Sleep(2 * time.Second)
	}

	if checkExitCode {
		g.ExpectWithOffset(2, session.ExitCode()).To(Equal(0))
	}

	return string(session.Out.Contents()) + string(session.Err.Contents())
}

// run a command until it exits 0
func (c Cluster) EventuallyKubectl(args ...string) string {
	args = append(args, "--context="+c.Context)
	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		command := exec.Command("kubectl", args...) //nolint:gosec
		session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
		content = string(session.Out.Contents())
	}, timeout, interval).Should(Succeed(), strings.Join(args, " "))
	return content
}

func (c Cluster) EventuallyKubectlDelete(args ...string) string {
	commandArgs := []string{"get", "--context=" + c.Context}
	commandArgs = append(commandArgs, args...)
	// #nosec
	command := exec.Command("kubectl", commandArgs...) //nolint:gosec
	session, err := gexec.Start(command, GinkgoWriter, GinkgoWriter)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	EventuallyWithOffset(1, session, time.Second*20).Should(gexec.Exit())
	//If it doesn't exist, lets succeed
	if strings.Contains(string(session.Err.Contents()), "not found") {
		return ""
	}

	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		commandArgs = []string{"delete", "--context=" + c.Context}
		commandArgs = append(commandArgs, args...)
		// #nosec
		cmd := exec.Command("kubectl", commandArgs...) //nolint:gosec
		session, err = gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
		content = string(session.Out.Contents())
	}, timeout, interval).Should(Succeed())
	return content
}

// ParseOutput parses the output of a kubectl command into the given v.
func ParseOutput(output string, v interface{}) {
	err := yaml.Unmarshal([]byte(output), v)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
}
