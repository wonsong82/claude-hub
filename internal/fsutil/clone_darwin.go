//go:build darwin

package fsutil

import "golang.org/x/sys/unix"

// cloneFile makes an APFS clone of src at dst.
func cloneFile(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}
