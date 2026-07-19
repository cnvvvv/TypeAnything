package ipc

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestPingRoundtrip starts a real pipe server with a stub handler, connects
// a Client, and verifies a ping comes back accepted. This is the smoke test
// that the C++ shim ↔ Go daemon transport works end-to-end.
//
// Each test uses a unique pipe name so parallel runs don't collide.
func TestPingRoundtrip(t *testing.T) {
	pipeName := `\\.\pipe\ta-test-` + filepath.Base(t.Name())

	handler := func(_ context.Context, req Request) (Response, error) {
		if req.Op != OpPing {
			t.Errorf("unexpected op %q", req.Op)
		}
		return Response{Accepted: true}, nil
	}

	srv := NewServer(pipeName, handler)
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.ListenAndServe() }()

	// Give the listener a moment to come up. DialPipe has a built-in retry
	// for ERROR_PIPE_BUSY, but the very first dial can race the bind.
	time.Sleep(100 * time.Millisecond)

	cli := NewClient(pipeName, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := cli.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}

	// Tear down cleanly so the goroutine exits.
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-srvErr:
		if err != nil {
			t.Fatalf("server returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server didn't exit after shutdown")
	}
}

// TestFrameRoundtrip exercises the length-prefixed codec directly,
// independent of any pipe. If this fails, the C++ side's framing is wrong too.
//
// net.Pipe is synchronous and unbuffered, so we run the reader in a goroutine
// and compare results after join.
func TestFrameRoundtrip(t *testing.T) {
	a, b := pipe(t)
	defer a.Close()
	defer b.Close()

	want := Request{Op: OpKey, Char: "a", KeyCode: 0x41}
	type result struct {
		got Request
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var got Request
		ch <- result{got: got, err: ReadFrame(b, &got)}
	}()

	if err := WriteFrame(a, &want); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatalf("ReadFrame: %v", r.err)
	}
	if r.got.Op != want.Op || r.got.Char != want.Char || r.got.KeyCode != want.KeyCode {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", r.got, want)
	}
}

// TestFrameTooLarge verifies the guard against bogus length headers,
// which is the main corruption risk on the wire.
func TestFrameTooLarge(t *testing.T) {
	a, b := pipe(t)
	defer a.Close()
	defer b.Close()

	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		var dst Request
		ch <- result{err: ReadFrame(b, &dst)}
	}()

	// Announce 2 MiB — over the 1 MiB ceiling.
	if _, err := a.Write([]byte{0x00, 0x00, 0x20, 0x00}); err != nil { // 0x200000 LE
		t.Fatalf("write: %v", err)
	}
	r := <-ch
	if r.err != ErrFrameTooLarge {
		t.Fatalf("got err=%v, want ErrFrameTooLarge", r.err)
	}
}
