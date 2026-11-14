//go:build !darwin && !linux

package fsutil

import "errors"

func cloneFile(src, dst string) error { return errors.New("clone not supported") }
