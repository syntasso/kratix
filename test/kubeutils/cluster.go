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

// run a command until it exits 0
func (c Cluster) EventuallyKubectl(args ...string) string {
	args = append(args, "--context="+c.Context)
	var content string
	EventuallyWithOffset(1, func(g Gomega) {
		command := exec.Command("kubectl", args...)
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
	command := exec.Command("kubectl", commandArgs...)
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
		cmd := exec.Command("kubectl", commandArgs...)
		session, err = gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
		g.ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
		g.EventuallyWithOffset(1, session, timeout, interval).Should(gexec.Exit(0))
		content = string(session.Out.Contents())
	}, timeout, time.Millisecond).Should(Succeed())
	return content
}
