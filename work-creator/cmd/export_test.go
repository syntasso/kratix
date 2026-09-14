package cmd

// UpdateStatus exposes the in-Job status writer to its spec: the whole
// read-modify-write, conflict retry included, is the unit under test.
var UpdateStatus = updateStatus
