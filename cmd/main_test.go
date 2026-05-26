/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestGetResourceBindingDefaultVersion(t *testing.T) {
	tests := []struct {
		name     string
		config   *KratixConfig
		expected ResourceBindingDefaultVersion
	}{
		{
			name:     "nil config defaults to floating",
			config:   nil,
			expected: ResourceBindingDefaultVersionFloating,
		},
		{
			name:     "config with no resourceBindings section defaults to floating",
			config:   &KratixConfig{},
			expected: ResourceBindingDefaultVersionFloating,
		},
		{
			name: "config with empty defaultVersion defaults to floating",
			config: &KratixConfig{
				ResourceBindings: &ResourceBindingsConfig{DefaultVersion: ""},
			},
			expected: ResourceBindingDefaultVersionFloating,
		},
		{
			name: "config with floating returns floating",
			config: &KratixConfig{
				ResourceBindings: &ResourceBindingsConfig{DefaultVersion: ResourceBindingDefaultVersionFloating},
			},
			expected: ResourceBindingDefaultVersionFloating,
		},
		{
			name: "config with pinned returns pinned",
			config: &KratixConfig{
				ResourceBindings: &ResourceBindingsConfig{DefaultVersion: ResourceBindingDefaultVersionPinned},
			},
			expected: ResourceBindingDefaultVersionPinned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(getResourceBindingDefaultVersion(tt.config)).To(Equal(tt.expected))
		})
	}
}
