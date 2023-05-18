package writers

type StateStoreWriter interface {
	WriteObject(objectName string, toWrite []byte) error
	RemoveObject(objectName string) error
}
