package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestVersionFlag verifies that `lapp --version` prints a non-empty version
// string and exits with code 0 without blocking on stdin.
func TestVersionFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}

	// Build the binary into a temp dir.
	tmpBin, err := os.CreateTemp("", "lapp-test-*")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	tmpBin.Close()
	defer os.Remove(tmpBin.Name())

	buildCmd := exec.Command("go", "build", "-o", tmpBin.Name(), ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, tmpBin.Name(), "--version")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("lapp --version failed: %v", err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		t.Error("--version output is empty")
	}
	t.Logf("version: %q", version)
}
