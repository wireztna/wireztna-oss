//go:build !windows

package cmd

func existingServiceOwnerAvailable() bool {
	return false
}

func acquireServiceOwnership() (func(), error) {
	return func() {}, nil
}
