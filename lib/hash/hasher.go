package hash

import (
	"crypto/md5"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func ComputeHash(input *unstructured.Unstructured) (string, error) {
	spec, err := json.Marshal(input.Object["spec"])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", md5.Sum(spec)), nil
}
