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
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("BucketStateStore Webhook", func() {
	var (
		obj       *v1alpha1.BucketStateStore
		oldObj    *v1alpha1.BucketStateStore
		validator BucketStateStoreCustomValidator
	)

	BeforeEach(func() {
		obj = &v1alpha1.BucketStateStore{}
		oldObj = &v1alpha1.BucketStateStore{}
		validator = BucketStateStoreCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("auth method is accessKey", func() {
		Context("create", func() {
			It("denies creation if secretRef not set", func() {
				obj.Spec.AuthMethod = "accessKey"
				obj.Spec.SecretRef = nil
				_, err := validator.ValidateCreate(context.TODO(), obj)
				Expect(err).To(MatchError("spec.secretRef must be set when using authentication method accessKey"))
			})

			It("denies creation if secretRef is missing 'name'", func() {
				obj.Spec.AuthMethod = "accessKey"
				obj.Spec.SecretRef = &corev1.SecretReference{
					Namespace: "set",
					Name:      "",
				}
				_, err := validator.ValidateCreate(context.TODO(), obj)
				Expect(err).To(MatchError("spec.secretRef must contain secret name"))
			})

			It("denies creation if secretRef is missing 'namespace'", func() {
				obj.Spec.AuthMethod = "accessKey"
				obj.Spec.SecretRef = &corev1.SecretReference{
					Namespace: "",
					Name:      "set",
				}
				_, err := validator.ValidateCreate(context.TODO(), obj)
				Expect(err).To(MatchError("spec.secretRef must contain secret namespace"))
			})
		})
		Context("update", func() {
			BeforeEach(func() {
				oldObj.Spec.AuthMethod = "accessKey"
				oldObj.Spec.SecretRef = &corev1.SecretReference{
					Namespace: "set",
					Name:      "set",
				}
				obj.Spec.AuthMethod = "accessKey"
			})

			It("denies update if secretRef not set", func() {
				obj.Spec.SecretRef = nil
				_, err := validator.ValidateUpdate(context.TODO(), oldObj, obj)
				Expect(err).To(MatchError("spec.secretRef must be set when using authentication method accessKey"))
			})

			It("denies creation if secretRef is missing 'name'", func() {
				obj.Spec.SecretRef = &corev1.SecretReference{
					Namespace: "set",
					Name:      "",
				}
				_, err := validator.ValidateUpdate(context.TODO(), oldObj, obj)
				Expect(err).To(MatchError("spec.secretRef must contain secret name"))
			})

			It("denies creation if secretRef is missing 'namespace'", func() {
				obj.Spec.SecretRef = &corev1.SecretReference{
					Namespace: "",
					Name:      "set",
				}
				_, err := validator.ValidateUpdate(context.TODO(), oldObj, obj)
				Expect(err).To(MatchError("spec.secretRef must contain secret namespace"))
			})
		})
	})

	Context("IAM", func() {
		It("succeed with no secretRef", func() {
			obj.Spec.AuthMethod = "IAM"
			obj.Spec.SecretRef = nil
			warnings, err := validator.ValidateCreate(context.TODO(), obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())

			warnings, err = validator.ValidateUpdate(context.TODO(), oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())
		})
	})
})
