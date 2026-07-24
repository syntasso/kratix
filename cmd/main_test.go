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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	gitutil "github.com/syntasso/kratix/util/git"
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

var _ = Describe("getGitMinimumFetchInterval", func() {
	It("returns the default when config is nil", func() {
		Expect(getGitMinimumFetchInterval(nil)).To(Equal(gitutil.DefaultMinimumFetchInterval))
	})

	It("returns the default when git config is not set", func() {
		Expect(getGitMinimumFetchInterval(&KratixConfig{})).To(Equal(gitutil.DefaultMinimumFetchInterval))
	})

	It("returns the default when minimumFetchInterval is not set", func() {
		config := &KratixConfig{Git: &GitConfig{}}
		Expect(getGitMinimumFetchInterval(config)).To(Equal(gitutil.DefaultMinimumFetchInterval))
	})

	It("returns zero when minimumFetchInterval is zero", func() {
		config := &KratixConfig{
			Git: &GitConfig{
				MinimumFetchInterval: &metav1.Duration{Duration: 0},
			},
		}
		Expect(getGitMinimumFetchInterval(config)).To(Equal(time.Duration(0)))
	})

	It("returns the configured minimumFetchInterval", func() {
		config := &KratixConfig{
			Git: &GitConfig{
				MinimumFetchInterval: &metav1.Duration{Duration: 30 * time.Second},
			},
		}
		Expect(getGitMinimumFetchInterval(config)).To(Equal(30 * time.Second))
	})

	It("returns the default when minimumFetchInterval is negative", func() {
		config := &KratixConfig{
			Git: &GitConfig{
				MinimumFetchInterval: &metav1.Duration{Duration: -1 * time.Second},
			},
		}
		Expect(getGitMinimumFetchInterval(config)).To(Equal(gitutil.DefaultMinimumFetchInterval))
	})
})
