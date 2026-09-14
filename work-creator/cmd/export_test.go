package cmd

// UpdateStatus exposes the in-Job status writer to its spec: the whole
// read-modify-write, including the conflict retry around it, is the unit under
// test, and it is not reachable from outside the package otherwise.
var UpdateStatus = updateStatus
