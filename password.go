package main

import (
	"io"
	"os"
	"strings"
)

// readPasswordLine reads a single line from stdin. Callers are responsible for
// putting the terminal into a no-echo mode first (see password_unix.go and
// password_windows.go) and restoring it afterwards.
func readPasswordLine(prompt string, stderr io.Writer) (string, error) {
	if stderr != nil && prompt != "" {
		if _, err := io.WriteString(stderr, prompt); err != nil {
			return "", err
		}
	}
	var builder strings.Builder
	buffer := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		if n == 0 {
			continue
		}
		if buffer[0] == '\n' || buffer[0] == '\r' {
			break
		}
		builder.WriteByte(buffer[0])
	}
	if stderr != nil {
		if _, err := io.WriteString(stderr, "\n"); err != nil {
			return "", err
		}
	}
	return builder.String(), nil
}
