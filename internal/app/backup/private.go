package backup

// ReadPrivate reads bounded regular 0600 inputs without following symlinks.
func ReadPrivate(path string) ([]byte, error) { return private(path) }
