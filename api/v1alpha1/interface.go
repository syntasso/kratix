package v1alpha1

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// +kubebuilder:object:generate=false

//counterfeiter:generate . PromiseFetcher
type PromiseFetcher interface {
	FromURL(string, string) (*Promise, error)
}
