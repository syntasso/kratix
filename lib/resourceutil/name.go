package resourceutil

import (
	"k8s.io/apimachinery/pkg/util/uuid"
)

const maxNameLength = 63 - 1 - 5 //five char sha, plus the separator

func GenerateObjectName(name string) string {
	if len(name) > maxNameLength {
		name = name[0 : maxNameLength-1]
	}

	id := uuid.NewUUID()

	return name + "-" + string(id[0:5])
}
