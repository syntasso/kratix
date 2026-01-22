// Derived from Argo CD (https://github.com/argoproj/argo-cd/commit/559dc16e7216dc8e250eeb69f78f749396a74e9f),
// licensed under the Apache License, Version 2.0.
//
// Modifications Copyright 2026 Syntasso

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"

	"github.com/syntasso/kratix/internal/logging"
)

var DefaultCmdOpts = CmdOpts{
	Timeout:          time.Duration(0),
	FatalTimeout:     time.Duration(0),
	Redactor:         Unredacted,
	TimeoutBehavior:  TimeoutBehavior{syscall.SIGKILL, false},
	SkipErrorLogging: false,
	CaptureStderr:    false,
}

type ExecRunOpts struct {
	// Redactor redacts tokens from the output.
	Redactor func(text string) string
	// TimeoutBehavior configures what to do in case of timeout.
	TimeoutBehavior TimeoutBehavior
	// SkipErrorLogging determines whether to skip logging of execution errors (rc > 0).
	SkipErrorLogging bool
	// CaptureStderr determines whether to capture stderr in addition to stdout.
	CaptureStderr bool
}

func RunWithExecRunOpts(cmd *exec.Cmd, opts ExecRunOpts, logger logr.Logger) (string, error) {
	cmdOpts := CmdOpts{
		Timeout:          timeout,
		FatalTimeout:     fatalTimeout,
		Redactor:         opts.Redactor,
		TimeoutBehavior:  opts.TimeoutBehavior,
		SkipErrorLogging: opts.SkipErrorLogging,
		CaptureStderr:    opts.CaptureStderr}
	return RunCommandExt(cmd, cmdOpts, logger)
}

type CmdError struct {
	Args   string
	Stderr string
	Cause  error
}

func (ce *CmdError) Error() string {
	res := fmt.Sprintf("`%v` failed %v", ce.Args, ce.Cause)
	if ce.Stderr != "" {
		res = fmt.Sprintf("%s: %s", res, ce.Stderr)
	}
	return res
}

func (ce *CmdError) String() string {
	return ce.Error()
}

func newCmdError(args string, cause error, stderr string) *CmdError {
	return &CmdError{Args: args, Stderr: stderr, Cause: cause}
}

// TimeoutBehavior defines behaviour for when the command takes longer than the passed in timeout to exit
// By default, SIGKILL is sent to the process and it is not waited upon
type TimeoutBehavior struct {
	// Signal determines the signal to send to the process
	Signal syscall.Signal
	// ShouldWait determines whether to wait for the command to exit once timeout is reached
	ShouldWait bool
}

type runOpts struct {
	SkipErrorLogging bool
	CaptureStderr    bool
}

type CmdOpts struct {
	// Timeout determines how long to wait for the command to exit
	Timeout time.Duration
	// FatalTimeout is the amount of additional time to wait after Timeout before fatal SIGKILL
	FatalTimeout time.Duration
	// Redactor redacts tokens from the output
	Redactor func(text string) string
	// TimeoutBehavior configures what to do in case of timeout
	TimeoutBehavior TimeoutBehavior
	// SkipErrorLogging defines whether to skip logging of execution errors (rc > 0)
	SkipErrorLogging bool
	// CaptureStderr defines whether to capture stderr in addition to stdout
	CaptureStderr bool
}

// RunCommandExt is a convenience function to run/log a command and return/log stderr in an error upon
// failure.
func RunCommandExt(cmd *exec.Cmd, opts CmdOpts, logger logr.Logger) (string, error) {
	execId, err := RandHex(5)
	if err != nil {
		return "", err
	}
	logCtx := logger.WithValues("execID", execId, "dir", cmd.Dir)

	redactor := DefaultCmdOpts.Redactor
	if opts.Redactor != nil {
		redactor = opts.Redactor
	}

	// log in a way we can copy-and-paste into a terminal
	args := strings.Join(cmd.Args, " ")
	logging.Debug(logCtx, "running command", "args", redactor(args))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err = cmd.Start()
	if err != nil {
		return "", err
	}

	done := make(chan error)
	go func() { done <- cmd.Wait() }()

	// Start timers for timeout
	timeout := DefaultCmdOpts.Timeout
	fatalTimeout := DefaultCmdOpts.FatalTimeout

	if opts.Timeout != time.Duration(0) {
		timeout = opts.Timeout
	}

	if opts.FatalTimeout != time.Duration(0) {
		fatalTimeout = opts.FatalTimeout
	}

	var timeoutCh <-chan time.Time
	if timeout != 0 {
		timeoutCh = time.NewTimer(timeout).C
	}

	var fatalTimeoutCh <-chan time.Time
	if fatalTimeout != 0 {
		fatalTimeoutCh = time.NewTimer(timeout + fatalTimeout).C
	}

	timeoutBehavior := DefaultCmdOpts.TimeoutBehavior
	fatalTimeoutBehaviour := syscall.SIGKILL
	if opts.TimeoutBehavior.Signal != syscall.Signal(0) {
		timeoutBehavior = opts.TimeoutBehavior
	}

	select {
	// noinspection ALL
	case <-timeoutCh:
		// send timeout signal
		_ = cmd.Process.Signal(timeoutBehavior.Signal)
		// wait on timeout signal and fallback to fatal timeout signal
		if timeoutBehavior.ShouldWait {
			select {
			case <-done:
			case <-fatalTimeoutCh:
				// upgrades to SIGKILL if cmd does not respect SIGTERM
				_ = cmd.Process.Signal(fatalTimeoutBehaviour)
				// now original cmd should exit immediately after SIGKILL
				<-done
				// return error with a marker indicating that cmd exited only after fatal SIGKILL
				output := stdout.String()
				if opts.CaptureStderr {
					output += stderr.String()
				}
				logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))
				err = newCmdError(redactor(args), fmt.Errorf("fatal timeout after %v", timeout+fatalTimeout), "")
				logging.Error(logCtx, err, "command failed", "args", redactor(args), "duration", time.Since(start))
				return strings.TrimSuffix(output, "\n"), err
			}
		}
		// either did not wait for timeout or cmd did respect SIGTERM
		output := stdout.String()
		if opts.CaptureStderr {
			output += stderr.String()
		}
		logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))
		err = newCmdError(redactor(args), fmt.Errorf("timeout after %v", timeout), "")
		logging.Error(logCtx, err, "command failed", "args", redactor(args), "duration", time.Since(start))
		return strings.TrimSuffix(output, "\n"), err
	case err := <-done:
		if err != nil {
			output := stdout.String()
			if opts.CaptureStderr {
				output += stderr.String()
			}
			logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))
			err := newCmdError(redactor(args), errors.New(redactor(err.Error())), strings.TrimSpace(redactor(stderr.String())))
			if !opts.SkipErrorLogging {
				logging.Error(logCtx, err, "command failed", "args", redactor(args), "duration", time.Since(start))
			}
			return strings.TrimSuffix(output, "\n"), err
		}
	}
	output := stdout.String()
	if opts.CaptureStderr {
		output += stderr.String()
	}
	logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))

	return strings.TrimSuffix(output, "\n"), nil
}

func (m *nativeGitClient) runCmdOutput(cmd *exec.Cmd, ropts runOpts) (string, error) {
	cmd.Dir = m.root
	cmd.Env = append(os.Environ(), cmd.Env...)
	// Set $HOME to nowhere, so we can execute Git regardless of any external
	// authentication keys (e.g. in ~/.ssh) -- this is especially important for
	// running tests on local machines and/or CircleCI.
	cmd.Env = append(cmd.Env, "HOME=/dev/null")
	// Skip LFS for most Git operations except when explicitly requested
	cmd.Env = append(cmd.Env, "GIT_LFS_SKIP_SMUDGE=1")
	// Disable Git terminal prompts in case we're running with a tty
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=false")
	// Add Git configuration options that are essential for the command to work
	cmd.Env = append(cmd.Env, m.gitConfigEnv...)

	cmd.Env = append(cmd.Env,
		"GIT_ASKPASS=true",   // Disable password prompts by setting it to the binary `/bin/true`
		"GIT_CONFIG_COUNT=1", // Number of config settings
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=", // Disable credential helper
	)

	// For HTTPS repositories, we need to consider insecure repositories as well
	// as custom CA bundles from the cert database.
	if IsHTTPSURL(m.repoURL) {
		if m.insecure {
			cmd.Env = append(cmd.Env, "GIT_SSL_NO_VERIFY=true")
		} else {
			parsedURL, err := url.Parse(m.repoURL)
			// We don't fail if we cannot parse the URL, but log a warning in that
			// case. And we execute the command in a verbatim way.
			if err != nil {
				logging.Warn(m.log, "could not parse repo URL", "repoURL", m.repoURL)
			} else {
				caPath := getCertBundlePathForRepository(parsedURL.Host)
				if err == nil && caPath != "" {
					cmd.Env = append(cmd.Env, "GIT_SSL_CAINFO="+caPath)
				}
			}
		}
	}
	cmd.Env = upsertProxyEnv(cmd, m.proxy, m.noProxy)
	opts := ExecRunOpts{
		TimeoutBehavior: TimeoutBehavior{
			Signal:     syscall.SIGTERM,
			ShouldWait: true,
		},
		SkipErrorLogging: ropts.SkipErrorLogging,
		CaptureStderr:    ropts.CaptureStderr,
	}
	return RunWithExecRunOpts(cmd, opts, m.log)
}

// runCmd is a convenience function to run a command in a given directory and return its output
func (m *nativeGitClient) runCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	return m.runCmdOutput(cmd, runOpts{})
}

// runCredentialedCmd is a convenience function to run a git command with username/password credentials
func (m *nativeGitClient) runCredentialedCmd(ctx context.Context, args ...string) error {
	closer, environ, err := m.creds.Environ(m.log)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	// If a basic auth header is explicitly set, tell Git to send it to the
	// server to force use of basic auth instead of negotiating the auth scheme
	for _, e := range environ {
		if strings.HasPrefix(e, forceBasicAuthHeaderEnv+"=") {
			args = append([]string{"--config-env", "http.extraHeader=" + forceBasicAuthHeaderEnv}, args...)
		} else if strings.HasPrefix(e, bearerAuthHeaderEnv+"=") {
			args = append([]string{"--config-env", "http.extraHeader=" + bearerAuthHeaderEnv}, args...)
		}
	}

	if m.accessToken != "" {
		urlWithCreds, err := injectGitHubAppCredentials(m.repoURL, m.accessToken)
		if err != nil {
			return err
		}
		args = append([]string{"-c", fmt.Sprintf("url.'%s'.insteadOf='%s'", urlWithCreds, m.repoURL)}, args...)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(cmd.Env, environ...)
	_, err = m.runCmdOutput(cmd, runOpts{})
	return err
}
