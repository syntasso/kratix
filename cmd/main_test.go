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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("getResourceBindingDefaultVersion", func() {
	It("returns floating when config is nil", func() {
		Expect(getResourceBindingDefaultVersion(nil)).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns floating when config has no resourceBindings section", func() {
		Expect(getResourceBindingDefaultVersion(&KratixConfig{})).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns floating when defaultVersion is empty", func() {
		config := &KratixConfig{
			ResourceBindings: &ResourceBindingsConfig{DefaultVersion: ""},
		}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns floating when defaultVersion is set to floating", func() {
		config := &KratixConfig{
			ResourceBindings: &ResourceBindingsConfig{DefaultVersion: ResourceBindingDefaultVersionFloating},
		}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns pinned when defaultVersion is set to pinned", func() {
		config := &KratixConfig{
			ResourceBindings: &ResourceBindingsConfig{DefaultVersion: ResourceBindingDefaultVersionPinned},
		}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionPinned))
	})

	It("returns floating for an unrecognised defaultVersion value", func() {
		config := &KratixConfig{
			ResourceBindings: &ResourceBindingsConfig{DefaultVersion: "invalid"},
		}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionFloating))
	})
})
