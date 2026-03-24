package lib

import (
	"errors"
	"fmt"
	"os"

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
