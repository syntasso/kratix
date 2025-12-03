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

package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("PromiseRevision Webhook", func() {
	var (
		obj       *platformv1alpha1.PromiseRevision
		validator PromiseRevisionCustomValidator
	)

	BeforeEach(func() {
		obj = &platformv1alpha1.PromiseRevision{}
		validator = PromiseRevisionCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	When("deleting PromiseRevision under Validating Webhook", func() {
		It("should deny deletion if revision .status.latest is true", func() {
			obj.Status.Latest = true
			Expect(validator.ValidateDelete(context.TODO(), obj)).Error().To(HaveOccurred())
		})

		It("allows deletion when revision .status.latest is not set to true", func() {
			Expect(validator.ValidateDelete(context.TODO(), obj)).Error().NotTo(HaveOccurred())
		})
	})

})
