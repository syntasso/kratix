package resourceutil

import "k8s.io/apimachinery/pkg/util/uuid"

const MAX_NAME_LENGTH = 63 - 1 - 5 //five char sha, plus the separator

func GenerateObjectName(name string) string {
	if len(name) > MAX_NAME_LENGTH {
		name = name[0 : MAX_NAME_LENGTH-1]
	}

	id := uuid.NewUUID()

	return name + "-" + string(id[0:5])
}
