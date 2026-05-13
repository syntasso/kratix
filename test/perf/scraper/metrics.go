// Package scraper polls the kratix controller's /metrics endpoint at a fixed
// interval and writes each snapshot to a file under the run's results directory.
//
// It shells out to `kubectl port-forward` rather than wiring up client-go SPDY
// because the rig is run from a developer workstation against a kind cluster
// and the subprocess approach is dramatically simpler.
package scraper

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Scraper struct {
	Context    string
	Namespace  string
	Deployment string
	TargetPort int
	// Scheme is "http" or "https". The deployment's metrics endpoint is
	// HTTPS-only when --metrics-secure=true (the default in the upstream
	// manifests).
	Scheme string
	// BearerToken is sent as Authorization: Bearer ... on every scrape.
	// Empty means no Authorization header. Required when the metrics
	// server uses WithAuthenticationAndAuthorization.
	BearerToken string
	Interval    time.Duration
	OutputDir   string

	startedAt time.Time
	localPort int
	pf        *exec.Cmd
	pfDone    chan struct{}
	client    *http.Client

	mu       sync.Mutex
	stopped  bool
	stopOnce sync.Once
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	err      error
}

func New(kubeContext, namespace, deployment string, targetPort int, outputDir string) *Scraper {
	return &Scraper{
		Context:    kubeContext,
		Namespace:  namespace,
		Deployment: deployment,
		TargetPort: targetPort,
		Scheme:     "http",
		Interval:   time.Second,
		OutputDir:  outputDir,
	}
}

// NewSecure builds a scraper for an HTTPS+TLS metrics endpoint that is
// protected by SubjectAccessReview. TLS verification is skipped (the metrics
// cert is signed by the cluster CA; for a local rig there's no point chasing
// it).
func NewSecure(kubeContext, namespace, deployment string, targetPort int, outputDir, bearerToken string) *Scraper {
	s := New(kubeContext, namespace, deployment, targetPort, outputDir)
	s.Scheme = "https"
	s.BearerToken = bearerToken
	return s
}

func (s *Scraper) Start(ctx context.Context) error {
	if err := os.MkdirAll(s.OutputDir, 0o755); err != nil {
		return fmt.Errorf("mkdir output: %w", err)
	}

	s.client = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: s.Scheme == "https"},
		},
	}

	port, err := pickLocalPort()
	if err != nil {
		return fmt.Errorf("pick local port: %w", err)
	}
	s.localPort = port

	s.pfDone = make(chan struct{})
	s.pf = exec.Command("kubectl",
		"--context="+s.Context,
		"-n", s.Namespace,
		"port-forward",
		"deploy/"+s.Deployment,
		fmt.Sprintf("%d:%d", s.localPort, s.TargetPort),
	)
	// route port-forward output to the run dir for debugging
	pfLog, err := os.Create(filepath.Join(s.OutputDir, "port-forward.log"))
	if err != nil {
		return fmt.Errorf("create pf log: %w", err)
	}
	s.pf.Stdout = pfLog
	s.pf.Stderr = pfLog
	if err := s.pf.Start(); err != nil {
		return fmt.Errorf("start port-forward: %w", err)
	}
	go func() {
		_ = s.pf.Wait()
		close(s.pfDone)
	}()

	if err := s.waitForReady(ctx, 15*time.Second); err != nil {
		s.killPF()
		return fmt.Errorf("port-forward not ready: %w", err)
	}

	pollCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.startedAt = time.Now()
	s.wg.Add(1)
	go s.loop(pollCtx)
	return nil
}

// Stop ends the scrape loop and tears down the port-forward. Safe to call
// multiple times.
func (s *Scraper) Stop() error {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.wg.Wait()
		s.killPF()
	})
	return s.err
}

func (s *Scraper) loop(ctx context.Context) {
	defer s.wg.Done()
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	// scrape immediately, then on ticks
	s.scrape(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scrape(ctx)
		}
	}
}

func (s *Scraper) scrape(ctx context.Context) {
	elapsed := int(time.Since(s.startedAt).Seconds())
	path := filepath.Join(s.OutputDir, fmt.Sprintf("metrics-T+%05ds.prom", elapsed))

	req, err := http.NewRequestWithContext(ctx, "GET", s.metricsURL(), nil)
	if err != nil {
		s.recordErr(err)
		return
	}
	if s.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.BearerToken)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.recordErr(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.recordErr(fmt.Errorf("metrics %s: %s", path, resp.Status))
		return
	}

	f, err := os.Create(path)
	if err != nil {
		s.recordErr(err)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		s.recordErr(err)
	}
}

func (s *Scraper) metricsURL() string {
	return fmt.Sprintf("%s://127.0.0.1:%d/metrics", s.Scheme, s.localPort)
}

func (s *Scraper) recordErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *Scraper) killPF() {
	if s.pf == nil || s.pf.Process == nil {
		return
	}
	_ = s.pf.Process.Kill()
	select {
	case <-s.pfDone:
	case <-time.After(2 * time.Second):
	}
}

func (s *Scraper) waitForReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := s.metricsURL()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.pfDone:
			return fmt.Errorf("port-forward exited before becoming ready (see port-forward.log)")
		default:
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		if s.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+s.BearerToken)
		}
		resp, err := s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

func pickLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
