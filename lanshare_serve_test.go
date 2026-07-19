package main

import (
	"os"
	"path/filepath"
	"testing"
)

// guardServePath must refuse to publish the home directory, any credential
// store, and system directories over `s2u --serve`, while still allowing an
// ordinary project subfolder.
func TestGuardServePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	home = filepath.Clean(home)

	blocked := []string{
		home,                                  // the whole home dir
		filepath.Dir(home),                    // an ancestor of home (e.g. /home)
		"/",                                   // filesystem root
		filepath.Join(home, ".ssh"),           // credential store
		filepath.Join(home, ".ssh", "id_rsa"), // a file inside one
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".config"),
		filepath.Join(home, ".config", "gcloud"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".npmrc"),
		"/etc",
		"/etc/passwd",
		"/root",
	}
	for _, p := range blocked {
		if err := guardServePath(p); err == nil {
			t.Errorf("guardServePath(%q) = nil, want a refusal", p)
		}
	}

	allowed := []string{
		filepath.Join(home, "projects", "mysite"),
		filepath.Join(home, "Downloads"),
		filepath.Join(home, "s2u-share"),
	}
	for _, p := range allowed {
		if err := guardServePath(p); err != nil {
			t.Errorf("guardServePath(%q) = %v, want nil", p, err)
		}
	}
}
