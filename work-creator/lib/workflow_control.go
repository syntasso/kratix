package lib

import (
	"errors"
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/yaml"
)

type WorkflowControl struct {
	Suspend    bool   `json:"suspend"`
	Message    string `json:"message"`
	RetryAfter string `json:"retryAfter"`
}

func ReadWorkflowControlFile(workflowControlFile string) (*WorkflowControl, error) {
	var workflowControl WorkflowControl
	if _, err := os.Stat(workflowControlFile); err == nil {
		bytes, err := os.ReadFile(workflowControlFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read "+
				"the workflow control file: %q with error: %w", workflowControlFile, err)
		}
		if err := yaml.Unmarshal(bytes, &workflowControl); err != nil {
			return nil, fmt.Errorf("failed to unmarshal "+
				"the workflow control file: %q with error: %w", workflowControlFile, err)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else {
		return nil, err
	}
	return &workflowControl, nil
}

func (w *WorkflowControl) IfSuspendOrRetry() bool {
	return w.IsSuspend() || w.IsRetry()
}

func (w *WorkflowControl) IsSuspend() bool {
	if w == nil {
		return false
	}
	return w.Suspend
}

func (w *WorkflowControl) IsRetry() bool {
	if w == nil {
		return false
	}

	return w.RetryAfter != ""
}

func (w *WorkflowControl) RetryDuration() (time.Duration, error) {
	if w == nil {
		return -1, fmt.Errorf("workflow control is nil")
	}

	if !w.IsRetry() {
		return -1, nil
	}

	d, err := time.ParseDuration(w.RetryAfter)
	if err != nil {
		return -1, fmt.Errorf("failed to parse retry duration: %q with error: %w", w.RetryAfter, err)
	}
	return d, nil
}
