package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDialOrgscaleSocketRetriesUntilPresent proves the CLI socket dialer
// tolerates a not-yet-created socket (the server-still-starting race) by
// retrying until it appears, rather than failing immediately like a bare dial.
func TestDialOrgscaleSocketRetriesUntilPresent(t *testing.T) {
	// Not t.TempDir(): its path under macOS's $TMPDIR, plus this test's name,
	// exceeds the 104-byte sun_path limit and bind fails with EINVAL.
	dir, err := os.MkdirTemp("", "hs-sock") //nolint:usetesting // t.TempDir() path is too long for a unix socket on macOS
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	sock := filepath.Join(dir, "orgscale.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type result struct {
		conn net.Conn
		err  error
	}

	done := make(chan result, 1)

	go func() {
		conn, err := dialOrgscaleSocket(ctx, sock)
		done <- result{conn, err}
	}()

	// Listen only after the dialer has begun, so its backoff must retry the
	// absent socket and connect once it exists.
	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "unix", sock)
	require.NoError(t, err)

	defer ln.Close()

	go func() {
		if conn, _ := ln.Accept(); conn != nil {
			conn.Close()
		}
	}()

	res := <-done
	require.NoError(t, res.err)
	require.NotNil(t, res.conn)

	res.conn.Close()
}

// TestDialOrgscaleSocketRespectsDeadline proves the retry is bounded by the
// context: when the socket never appears, the dialer returns an error around the
// deadline instead of hanging.
func TestDialOrgscaleSocketRespectsDeadline(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()

	conn, err := dialOrgscaleSocket(ctx, sock)
	require.Error(t, err)
	assert.Nil(t, conn)
	assert.Less(t, time.Since(start), 5*time.Second, "should stop near the deadline, not hang")
}
