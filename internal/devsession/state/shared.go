package state

// WriteJSONAtomic writes value as private (mode 0600) JSON through a
// same-directory temporary file and rename.
func WriteJSONAtomic(path string, value any) error { return writeJSONAtomic(path, value) }

// ReadJSON strictly decodes one JSON file.
func ReadJSON[T any](path string) (T, error) { return readJSON[T](path) }
