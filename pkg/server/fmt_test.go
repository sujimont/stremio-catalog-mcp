package server

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGofmt(t *testing.T) {
	gofmtPath, err := exec.LookPath("gofmt")
	if err != nil {
		t.Skip("gofmt not found in PATH, skipping format check")
	}

	cmd := exec.Command(gofmtPath, "-l", "../..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run gofmt: %v\nOutput: %s", err, string(out))
	}
	output := strings.TrimSpace(string(out))
	if len(output) > 0 {
		t.Errorf("unformatted files detected by gofmt in repository:\n%s\nRun 'make fmt' or 'gofmt -w .' to fix.", output)
	}
}
