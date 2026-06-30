//go:build darwin || linux

package governor

import (
	"os"
	"syscall"
	"unsafe"
)

// IsForegrounded returns true when the process is the foreground process group
// leader of its controlling terminal, or when there is no tty (daemon mode).
func IsForegrounded() bool {
	fd := int(os.Stdin.Fd())
	var pgrp int
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		syscall.TIOCGPGRP, uintptr(unsafe.Pointer(&pgrp)))
	if errno != 0 {
		return true // no tty — daemon launched explicitly, always contribute
	}
	return pgrp == syscall.Getpgrp()
}
