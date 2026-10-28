package delivery_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
)

var _ = Describe("the built-in writer", func() {
	var (
		base   *writersfakes.FakeStateStoreWriter
		writer delivery.Writer
	)

	BeforeEach(func() {
		base = &writersfakes.FakeStateStoreWriter{}
		factory, err := delivery.For(delivery.BuiltIn)
		Expect(err).NotTo(HaveOccurred())
		writer, err = factory(base, nil, nil)
		Expect(err).NotTo(HaveOccurred())
	})

	It("writes through the state store's own writer and reports the version as delivered", func() {
		base.UpdateFilesReturns("version-1", nil)
		create := []v1alpha1.Workload{{Filepath: "a.yaml", Content: "a: 1"}}

		result, err := writer.Deliver(context.Background(), delivery.Target{
			Dir: "dir/", WorkPlacement: "wp-1", Promise: "p",
		}, create, []string{"old.yaml"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(delivery.Result{Outcome: delivery.Delivered, VersionID: "version-1"}))
		Expect(base.UpdateFilesCallCount()).To(Equal(1))
		dir, wp, gotCreate, gotRemove := base.UpdateFilesArgsForCall(0)
		Expect(dir).To(Equal("dir/"))
		Expect(wp).To(Equal("wp-1"))
		Expect(gotCreate).To(Equal(create))
		Expect(gotRemove).To(Equal([]string{"old.yaml"}))
	})

	It("returns the write error", func() {
		base.UpdateFilesReturns("", errors.New("push rejected"))

		_, err := writer.Deliver(context.Background(), delivery.Target{}, nil, nil)

		Expect(err).To(MatchError("push rejected"))
	})

	It("does not recognise any receipt, since it never issues one", func() {
		_, err := writer.Check(context.Background(), "receipt-1")

		Expect(err).To(MatchError(delivery.ErrUnknownReceipt))
		Expect(err).To(MatchError(ContainSubstring("receipt-1")))
	})

	It("writes the canary files", func() {
		canary := []v1alpha1.Workload{{Filepath: "canary.yaml", Content: "c: 1"}}

		written, err := writer.WriteCanary(context.Background(), "kratix-canary", canary)

		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(BeTrue())
		Expect(base.UpdateFilesCallCount()).To(Equal(1))
		dir, name, create, remove := base.UpdateFilesArgsForCall(0)
		Expect(dir).To(Equal(""))
		Expect(name).To(Equal("kratix-canary"))
		Expect(create).To(Equal(canary))
		Expect(remove).To(BeNil())
	})

	It("returns the canary write error", func() {
		base.UpdateFilesReturns("", errors.New("no access"))

		_, err := writer.WriteCanary(context.Background(), "kratix-canary", nil)

		Expect(err).To(MatchError("no access"))
	})

	It("removes the canary files", func() {
		base.DeleteFilesReturns(errors.New("delete failed"))

		err := writer.RemoveCanary(context.Background(), "kratix-canary", []string{"canary.yaml"})

		Expect(err).To(MatchError("delete failed"))
		Expect(base.DeleteFilesCallCount()).To(Equal(1))
		name, files := base.DeleteFilesArgsForCall(0)
		Expect(name).To(Equal("kratix-canary"))
		Expect(files).To(Equal([]string{"canary.yaml"}))
	})
})
