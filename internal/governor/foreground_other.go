//go:build !darwin && !linux

package governor

// IsForegrounded always returns true on non-Unix platforms.
func IsForegrounded() bool {
	return true
}
