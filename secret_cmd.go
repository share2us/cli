package main

// `s2u secret get <id>` (ADR-054 phase 3): a member hands this account's agent a secret in
// a direct message. The portal seals it to the account key; this device opens it with its
// keyring and saves the value to a private file. The value is never printed, so it never
// lands in an agent's transcript or a terminal scrollback.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	clicore "github.com/share2us/cli-core"
)

// secretIDPattern: the browser picks a UUID; anything else is refused before it becomes a
// URL path or a file name.
var secretIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

const noAccountKeyMessage = "this device has no account key; run `%s keys` (or approve it in the portal)"

func (a app) secret(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return a.secretUsage(len(args) == 0)
	}
	switch args[0] {
	case "get":
		return a.secretGet(ctx, args[1:])
	default:
		fmt.Fprintf(a.stderr, "unknown secret command %q\n", args[0])
		return a.secretUsage(true)
	}
}

func (a app) secretUsage(isErr bool) int {
	w, code := a.stdout, 0
	if isErr {
		w, code = a.stderr, 2
	}
	fmt.Fprintf(w, "usage: %s secret get <id> [-o PATH] [--force]\n", commandName)
	fmt.Fprintln(w, "  Saves a secret someone shared with your agent in a direct message to a file only")
	fmt.Fprintln(w, "  you can read (default: your Share2Us config folder, secrets/<id>). Prints the label")
	fmt.Fprintln(w, "  and the path, never the value. --force replaces an existing file.")
	return code
}

func (a app) secretGet(ctx context.Context, args []string) int {
	var id, out string
	force := false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--force" || arg == "-f":
			force = true
		case arg == "-o" || arg == "--output" || arg == "--out":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return a.secretUsage(true)
			}
			i++
			out = args[i]
		case strings.HasPrefix(arg, "--output="):
			out = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-") && arg != "-":
			fmt.Fprintf(a.stderr, "unknown flag %q\n", arg)
			return a.secretUsage(true)
		case id == "":
			id = strings.TrimSpace(arg)
		default:
			return a.secretUsage(true)
		}
	}
	if id == "" {
		return a.secretUsage(true)
	}
	if !secretIDPattern.MatchString(id) {
		fmt.Fprintf(a.stderr, "%q is not a secret id\n", id)
		return 2
	}

	defaultPath := out == ""
	path := out
	if defaultPath {
		base, err := os.UserConfigDir()
		if err != nil {
			return a.fail("find the config folder", err)
		}
		path = filepath.Join(base, "share2us", "secrets", id)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return a.fail("secret file", err)
	}
	// Check before fetching, so a refused overwrite doesn't mark the secret as fetched.
	if !force {
		if _, err := os.Lstat(path); err == nil {
			fmt.Fprintf(a.stderr, "%s already exists; pass --force to replace it, or -o to choose another file\n", path)
			return 1
		}
	}

	sn, _, err := a.secureSession(ctx)
	if err != nil {
		return a.fail("secret", err)
	}
	ak, err := sn.AccountKey(ctx)
	if err != nil {
		if errors.Is(err, clicore.ErrKeysNotEnrolled) || errors.Is(err, clicore.ErrDeviceNotApproved) {
			fmt.Fprintf(a.stderr, noAccountKeyMessage+"\n", commandName)
			return 1
		}
		return a.fail("open the account key", err)
	}
	s, err := sn.Keys.Client.GetDMSecret(ctx, id)
	if err != nil {
		var apiErr *clicore.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			fmt.Fprintf(a.stderr, "secret %s: not found (it may be for another account, or the id is wrong)\n", id)
			return 1
		}
		return a.fail("fetch the secret", err)
	}
	if s.ID != id || (s.RecipientAccountID != "" && s.RecipientAccountID != sn.Keys.AccountID) {
		fmt.Fprintf(a.stderr, "secret %s: the server returned a secret that isn't this one for this account; refusing it\n", id)
		return 1
	}
	label, value, err := clicore.OpenDMSecret(ak, s)
	if err != nil {
		if errors.Is(err, clicore.ErrDMSecretRemoved) {
			fmt.Fprintf(a.stderr, "secret %s was removed by its sender or owner\n", id)
			return 1
		}
		return a.fail("open the secret", err)
	}
	if err := writePrivateFile(path, []byte(value), force, defaultPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(a.stderr, "%s already exists; pass --force to replace it, or -o to choose another file\n", path)
			return 1
		}
		return a.fail("save the secret", err)
	}
	fmt.Fprintf(a.stdout, "Saved secret %q to %s (only you can read it).\n", printableLabel(label), path)
	return 0
}

// writePrivateFile writes data to a 0600 file. Without force it never replaces an existing
// file (or follows a symlink there); with force it writes a temp file and renames it over,
// so the old file's mode never leaks into the new one. The default secrets folder is kept
// at 0700.
func writePrivateFile(path string, data []byte, force, privateDir bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if privateDir {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	if !force {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(path)
			return err
		}
		return f.Close()
	}
	tmp, err := os.CreateTemp(dir, ".secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// printableLabel drops control characters: the label comes from another person.
func printableLabel(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
