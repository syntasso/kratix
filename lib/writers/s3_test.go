package writers_test

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("S3", func() {
	Describe("NewS3Writer", func() {
		var (
			logger         logr.Logger
			dest           v1alpha1.Destination
			stateStoreSpec v1alpha1.BucketStateStoreSpec
		)

		BeforeEach(func() {
			logger = ctrl.Log.WithName("setup")
			stateStoreSpec = v1alpha1.BucketStateStoreSpec{
				Endpoint:   "example.com",
				Insecure:   true,
				AuthMethod: writers.AuthMethodAccessKey,
				BucketName: "test-bucket-name",
				StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
					Path: "state-store-path",
				},
			}

			dest = v1alpha1.Destination{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test",
				},
				Spec: v1alpha1.DestinationSpec{
					Path: "dst-path/",
				},
			}

		})

		Describe("NewS3Writer", func() {
			It("should return a valid S3Writer", func() {
				creds := map[string][]byte{
					"accessKeyID":     []byte("accessKeyID"),
					"secretAccessKey": []byte("secretAccessKey"),
				}
				w, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).NotTo(HaveOccurred())
				Expect(w).NotTo(BeNil())

				Expect(w).To(BeAssignableToTypeOf(&writers.S3Writer{}))
				s3Writer, ok := w.(*writers.S3Writer)
				Expect(ok).To(BeTrue())

				Expect(s3Writer.BucketName).To(Equal("test-bucket-name"))
				Expect(s3Writer.Path).To(Equal("state-store-path/dst-path"))
			})

		})

		Context("accessKey", func() {
			BeforeEach(func() {
				stateStoreSpec.AuthMethod = "accessKey"
			})

			It("should return a valid S3Writer", func() {
				creds := map[string][]byte{
					"accessKeyID":     []byte("accessKeyID"),
					"secretAccessKey": []byte("secretAccessKey"),
				}
				_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).NotTo(HaveOccurred())
			})

			When("authMethod is empty", func() {
				It("should return a valid S3Writer", func() {
					creds := map[string][]byte{
						"accessKeyID":     []byte("accessKeyID"),
						"secretAccessKey": []byte("secretAccessKey"),
					}
					stateStoreSpec.AuthMethod = ""
					_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, creds)
					Expect(err).NotTo(HaveOccurred())
				})
			})

			When("accessKeyID is missing", func() {
				It("errors", func() {
					creds := map[string][]byte{
						"secretAccessKey": []byte("secretAccessKey"),
					}

					_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, creds)
					Expect(err).To(MatchError("missing key accessKeyID"))
				})
			})

			When("secretAccessKey is missing", func() {
				It("errors", func() {
					creds := map[string][]byte{
						"accessKeyID": []byte("accessKeyID"),
					}

					_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, creds)
					Expect(err).To(MatchError("missing key secretAccessKey"))
				})
			})

			When("creds is missing", func() {
				It("errors", func() {
					_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, nil)
					Expect(err).To(MatchError("secret not provided"))
				})
			})
		})

		Context("IAM", func() {
			BeforeEach(func() {
				stateStoreSpec.AuthMethod = "IAM"
			})

			It("should return a valid S3Writer", func() {
				_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, nil)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("unknown authMethod", func() {
			BeforeEach(func() {
				stateStoreSpec.AuthMethod = "foo"
			})

			It("should return a valid S3Writer", func() {
				_, err := writers.NewS3Writer(logger, stateStoreSpec, dest.Spec.Path, nil)
				Expect(err).To(MatchError("unknown authMethod foo"))
			})
		})

	})

})
