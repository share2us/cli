//go:build windows

package main

import (
	"io"
	"os"
	"path/filepath"
)

// replaceExecutable swaps in the new binary on Windows.
//
// Windows refuses to overwrite the image of a running process, so the usual
// rename-over-the-target trick fails with a sharing violation. It does allow
// the running image to be *renamed*, so park the current binary alongside
// itself, move the new one into place, then try to delete the old one. The old
// file stays locked until this process exits, so a leftover ".old" is expected
// and is cleared by the next update.
func replaceExecutable(target, source string) error {
	if _, err := os.Stat(target); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	// Stage via a random-named O_CREATE|O_EXCL file (os.CreateTemp) so a
	// pre-planted file at a predictable path cannot redirect the write.
	out, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".new-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	old := target + ".old"
	os.Remove(old) // best effort: clear a leftover from a previous update
	if err := os.Rename(target, old); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Rename(old, target) // put the working binary back
		os.Remove(tmp)
		return err
	}
	os.Remove(old) // locked while we run; the next update cleans it up
	return nil
}
