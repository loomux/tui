package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Loomux/tui/internal/version"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	want := "loomux " + version.Client() + "\nserver pin " + version.ServerPin + "\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"login"}, {"version", "extra"}, {"-h"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("%q: stdout = %q, want empty", args, stdout.String())
		}
		if !strings.HasPrefix(stderr.String(), "usage: loomux") {
			t.Errorf("%q: stderr = %q, want usage", args, stderr.String())
		}
	}
}
