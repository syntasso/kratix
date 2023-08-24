package writers

type ToWrite struct {
	Name    string
	Content []byte
}

type StateStoreWriter interface {
	WriteObjects(...ToWrite) error
	RemoveObject(objectName string) error
}
