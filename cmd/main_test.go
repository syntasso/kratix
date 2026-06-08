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

var _ = Describe("jobCacheSelector", func() {
	It("returns a selector matching managed-by=Kratix", func() {
		selector := jobCacheSelector()
		Expect(selector.String()).To(Equal("app.kubernetes.io/managed-by=Kratix"))
	})
})

var _ = Describe("getResourceBindingDefaultVersion", func() {
	It("returns floating when config is nil", func() {
		Expect(getResourceBindingDefaultVersion(nil)).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns floating when config has no resourceBindingVersionStrategy set", func() {
		Expect(getResourceBindingDefaultVersion(&KratixConfig{})).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns floating when resourceBindingVersionStrategy is empty", func() {
		config := &KratixConfig{ResourceBindingVersionStrategy: ""}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns floating when resourceBindingVersionStrategy is set to floating", func() {
		config := &KratixConfig{ResourceBindingVersionStrategy: ResourceBindingDefaultVersionFloating}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionFloating))
	})

	It("returns pinned when resourceBindingVersionStrategy is set to pinned", func() {
		config := &KratixConfig{ResourceBindingVersionStrategy: ResourceBindingDefaultVersionPinned}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionPinned))
	})

	It("returns floating for an unrecognised resourceBindingVersionStrategy value", func() {
		config := &KratixConfig{ResourceBindingVersionStrategy: "invalid"}
		Expect(getResourceBindingDefaultVersion(config)).To(Equal(ResourceBindingDefaultVersionFloating))
	})
})
