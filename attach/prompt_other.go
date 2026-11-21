//go:build !unix

package attach

import (
	"context"
	"os"
)

// pollLine: only Unix has a read of a file that a context can end. Here
// readLine reads from a goroutine it may leave behind.
func pollLine(context.Context, *os.File) (line []byte, ok, polled bool) { return nil, false, false }
