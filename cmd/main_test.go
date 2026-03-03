package main

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestGetPodTTLSecondsAfterFinished(t *testing.T) {
	t.Run("returns nil when config is nil", func(t *testing.T) {
		got := getPodTTLSecondsAfterFinished(nil)
		if got != nil {
			t.Fatalf("expected nil pod ttl, got %v", *got)
		}
	})

	t.Run("returns nil when ttl is not configured", func(t *testing.T) {
		got := getPodTTLSecondsAfterFinished(&KratixConfig{})
		if got != nil {
			t.Fatalf("expected nil pod ttl, got %v", *got)
		}
	})

	t.Run("returns nil when ttl is zero", func(t *testing.T) {
		got := getPodTTLSecondsAfterFinished(&KratixConfig{
			Workflows: Workflows{
				JobOptions: JobOptions{
					PodTTLSecondsAfterFinished: &metav1.Duration{Duration: 0},
				},
			},
		})
		if got != nil {
			t.Fatalf("expected nil pod ttl, got %v", *got)
		}
	})

	t.Run("returns nil when ttl is negative", func(t *testing.T) {
		got := getPodTTLSecondsAfterFinished(&KratixConfig{
			Workflows: Workflows{
				JobOptions: JobOptions{
					PodTTLSecondsAfterFinished: &metav1.Duration{Duration: -1 * time.Second},
				},
			},
		})
		if got != nil {
			t.Fatalf("expected nil pod ttl, got %v", *got)
		}
	})

	t.Run("returns configured ttl", func(t *testing.T) {
		expected := 60 * time.Second
		got := getPodTTLSecondsAfterFinished(&KratixConfig{
			Workflows: Workflows{
				JobOptions: JobOptions{
					PodTTLSecondsAfterFinished: &metav1.Duration{Duration: expected},
				},
			},
		})
		if got == nil {
			t.Fatalf("expected pod ttl %v, got nil", expected)
		}
		if *got != expected {
			t.Fatalf("expected pod ttl %v, got %v", expected, *got)
		}
	})
}

func TestPodTTLUnmarshalFromKratixConfig(t *testing.T) {
	config := `
workflows:
  jobOptions:
    podTTLSecondsAfterFinished: 60s
`

	var kratixConfig KratixConfig
	if err := yaml.Unmarshal([]byte(config), &kratixConfig); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	got := getPodTTLSecondsAfterFinished(&kratixConfig)
	if got == nil {
		t.Fatalf("expected pod ttl after finished to be configured")
	}
	if *got != time.Minute {
		t.Fatalf("expected pod ttl %v, got %v", time.Minute, *got)
	}
}
