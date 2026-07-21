//go:build linux || darwin

package main

import (
	"io"
	"syscall"

	"golang.org/x/sys/unix"
)

func readPasswordNoEcho(prompt string, stderr io.Writer) (string, error) {
	fd := int(syscall.Stdin)
	original, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return "", err
	}
	noEcho := *original
	noEcho.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &noEcho); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(fd, ioctlWriteTermios, original)

	return readPasswordLine(prompt, stderr)
}
