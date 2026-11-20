package main

import "testing"

func TestBrowserCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{"macOS", "darwin", nil, "open"},
		{"Linux under X", "linux", map[string]string{"DISPLAY": ":0"}, "xdg-open"},
		{"Linux under Wayland", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, "xdg-open"},
		// A terminal on a remote machine, a container: there is no
		// display to open a browser on, and xdg-open would start a text
		// browser in this terminal.
		{"Linux with no display", "linux", nil, ""},
		{"a BSD under X", "freebsd", map[string]string{"DISPLAY": ":0"}, "xdg-open"},
		{"Windows", "windows", nil, ""},
	} {
		got := browserCommand(tc.goos, func(k string) string { return tc.env[k] })
		if got != tc.want {
			t.Errorf("%s: browserCommand = %q, want %q", tc.name, got, tc.want)
		}
	}
}
