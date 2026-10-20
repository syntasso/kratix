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
	"encoding/json"
	"reflect"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// marshalStatusWithCustomFields adds the custom fields after the typed ones, which win on a clash.
func marshalStatusWithCustomFields(typed any, custom map[string]apiextensionsv1.JSON, typedFieldNames map[string]bool) ([]byte, error) {
	data, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	if len(custom) == 0 {
		return data, nil
	}

	fields := map[string]json.RawMessage{}
	if err = json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range custom {
		if typedFieldNames[key] {
			continue
		}
		fields[key] = value.Raw
	}
	return json.Marshal(fields)
}

// unmarshalStatusKeepingCustomFields decodes the typed fields and returns every other key as custom.
func unmarshalStatusKeepingCustomFields(data []byte, typed any, typedFieldNames map[string]bool) (map[string]apiextensionsv1.JSON, error) {
	if err := json.Unmarshal(data, typed); err != nil {
		return nil, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}

	var custom map[string]apiextensionsv1.JSON
	for key, value := range fields {
		if typedFieldNames[key] {
			continue
		}
		if custom == nil {
			custom = map[string]apiextensionsv1.JSON{}
		}
		custom[key] = apiextensionsv1.JSON{Raw: value}
	}
	return custom, nil
}

// jsonFieldNames returns the JSON names of a struct's fields.
func jsonFieldNames(structType reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := range structType.NumField() {
		name, _, _ := strings.Cut(structType.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}
