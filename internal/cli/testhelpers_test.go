package cli

import (
	"bytes"
	"strings"
	"testing"
)

// captureRun executes the CLI in-process and returns its stdout.
func captureRun(t *testing.T, args []string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	if code != 0 {
		return out.String(), &exitError{code: code, message: strings.TrimSpace(errOut.String())}
	}
	return out.String(), nil
}

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }
