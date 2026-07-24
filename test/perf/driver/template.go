package driver

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// PromiseTemplate is a Promise yaml file from which N variants can be derived
// by appending an index suffix to the Promise name and CRD GVK names.
type PromiseTemplate struct {
	raw []byte
}

// LoadPromiseTemplate reads a Promise YAML from disk for later materialisation.
func LoadPromiseTemplate(path string) (*PromiseTemplate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read promise yaml: %w", err)
	}
	return &PromiseTemplate{raw: raw}, nil
}

// GVKInfo captures the API surface for a materialised Promise — the rig
// needs Group/Version/Kind to create RRs against the dynamic CRD.
type GVKInfo struct {
	PromiseName string
	Group       string
	Version     string
	Kind        string
	Plural      string
}

// Materialise returns a Promise object with name + CRD names suffixed by index.
// Substitutions performed (example for original Promise name "simpleapp" and CRD kind "SimpleApp"):
//   - metadata.name: "simpleapp" → "simpleapp-NN"
//   - spec.api.metadata.name: "simpleapps.compound.kratix.io" → "simpleappNNs.compound.kratix.io"
//   - spec.api.spec.names.kind: "SimpleApp" → "SimpleAppNN"
//   - spec.api.spec.names.plural: "simpleapps" → "simpleappNNs"
//   - spec.api.spec.names.singular: "simpleapp" → "simpleappNN"
//
// NN is two zero-padded digits. The original Promise name and kind are detected
// from the template itself, not hardcoded.
func (t *PromiseTemplate) Materialise(index int) (*platformv1alpha1.Promise, *GVKInfo, error) {
	p := &platformv1alpha1.Promise{}
	if err := yaml.Unmarshal(t.raw, p); err != nil {
		return nil, nil, fmt.Errorf("unmarshal promise: %w", err)
	}
	originalName := p.GetName()
	suffix := fmt.Sprintf("-%02d", index)
	p.SetName(originalName + suffix)

	// Decode the embedded CRD so we can rename it.
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.Unmarshal(p.Spec.API.Raw, crd); err != nil {
		return nil, nil, fmt.Errorf("unmarshal embedded CRD: %w", err)
	}

	indexSuffix := fmt.Sprintf("%02d", index)
	// Sanitise to avoid any chars that wouldn't survive in a CRD plural/singular.
	indexSuffix = sanitise(indexSuffix)

	originalSingular := crd.Spec.Names.Singular
	originalPlural := crd.Spec.Names.Plural
	originalKind := crd.Spec.Names.Kind

	newSingular := originalSingular + indexSuffix
	var newPlural string
	if strings.HasSuffix(originalPlural, "s") {
		newPlural = originalSingular + indexSuffix + "s"
	} else {
		newPlural = originalPlural + indexSuffix
	}
	newKind := originalKind + indexSuffix

	crd.Spec.Names.Singular = newSingular
	crd.Spec.Names.Plural = newPlural
	crd.Spec.Names.Kind = newKind
	crd.SetName(newPlural + "." + crd.Spec.Group)

	// Re-encode and reattach to the Promise. Promise.Spec.API is a
	// *runtime.RawExtension which the kube client serialises through JSON,
	// so encode the CRD as JSON (not YAML).
	encoded, err := json.Marshal(crd)
	if err != nil {
		return nil, nil, fmt.Errorf("re-marshal CRD: %w", err)
	}
	p.Spec.API.Raw = encoded

	if len(crd.Spec.Versions) == 0 {
		return nil, nil, fmt.Errorf("CRD %s has no versions", crd.GetName())
	}

	return p, &GVKInfo{
		PromiseName: p.GetName(),
		Group:       crd.Spec.Group,
		Version:     crd.Spec.Versions[0].Name,
		Kind:        newKind,
		Plural:      newPlural,
	}, nil
}

// sanitise strips any non-alphanumeric characters. Some Promise names contain
// hyphens that don't survive in CRD plural names.
func sanitise(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
