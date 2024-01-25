package v1alpha1

// +kubebuilder:object:generate=false
//
//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 . PromiseFetcher
type PromiseFetcher interface {
	FromURL(string) (*Promise, error)
}
