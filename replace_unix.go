//go:build !windows

package main

import (
	"io"
	"os"
	"path/filepath"
)

func replaceExecutable(target, source string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	// Stage via a random-named O_CREATE|O_EXCL file (os.CreateTemp) so a symlink
	// pre-planted at a predictable ".<name>.new" path cannot redirect the write.
	out, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".new-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	if err := out.Chmod(info.Mode().Perm() | 0o111); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
