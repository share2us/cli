//go:build windows

package main

import (
	"io"
	"syscall"

	"golang.org/x/sys/windows"
)

func readPasswordNoEcho(prompt string, stderr io.Writer) (string, error) {
	handle := windows.Handle(syscall.Stdin)
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		return "", err
	}
	// Drop ECHO but keep LINE_INPUT so the console still handles backspace
	// editing, and PROCESSED_INPUT so Ctrl-C still interrupts.
	noEcho := original &^ windows.ENABLE_ECHO_INPUT
	noEcho |= windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT
	if err := windows.SetConsoleMode(handle, noEcho); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(handle, original)

	return readPasswordLine(prompt, stderr)
}
