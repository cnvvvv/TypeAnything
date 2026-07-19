package ipc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/Microsoft/go-winio"
)

// Handler processes one Request and returns a Response. The daemon installs
// a single Handler that dispatches on req.Op to the engines. Returning an
// error (rather than setting Response.Error) is for transport-level failures;
// business errors should go in Response.Error and return (Response{}, nil).
type Handler func(ctx context.Context, req Request) (Response, error)

// Server listens on the named pipe and dispatches each connection to the
// configured Handler. Each TSF shim instance holds one long-lived connection
// for the lifetime of its host process — there is no per-request reconnect
// (reconnecting on every keystroke would blow the latency budget).
type Server struct {
	pipeName string
	handler  Handler
	ln       net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	closed   bool
}

// NewServer creates — but does not start — a pipe server.
//
// SDDL: by default we use a permissive ACE that grants GENERIC_ALL to
// "Everyone" (WD) and "Anonymous" (AN). This is the simplest way to allow
// medium-integrity-level app processes (Explorer-launched notepad, chrome,
// etc.) to connect when the daemon itself happens to run at high IL
// (e.g. launched from an elevated shell during development). On a single-
// user machine this is fine; the pipe only accepts local connections and
// carries no secrets. Tighten in production if needed.
//
// We deliberately do NOT include the username in the pipe path: RDP
// sessions get their own \\.pipe namespace per session, and on a single-
// user machine the bare name is simpler for the C++ client.
func NewServer(pipeName string, handler Handler) *Server {
	return &Server{pipeName: pipeName, handler: handler}
}

// DefaultSDDL grants access to medium-IL callers without requiring the
// daemon to be launched via the explorer-token trick Weasel uses. ACEs:
//   D:P                          — DACL, protected (no inherit)
//   (A;;GA;;;WD)                 — Everyone: GENERIC_ALL
//   (A;;GA;;;AN)                 — Anonymous: GENERIC_ALL (some services)
//   (A;;0x12019b;;;AC)           — AppContainer: read/write/execute
//
// Reuse Weasel's medium-IL story if you'd rather restrict this — but the
// bare-name local pipe is already inside the user's trust boundary.
const DefaultSDDL = "D:P(A;;GA;;;WD)(A;;GA;;;AN)(A;;0x12019b;;;AC)"

// ListenAndServe starts the pipe listener and blocks until Shutdown is
// called or the listener fails permanently.
func (s *Server) ListenAndServe() error {
	cfg := &winio.PipeConfig{
		SecurityDescriptor: DefaultSDDL,
		MessageMode:        false, // byte mode — length-prefixed framing
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	}
	ln, err := winio.ListenPipe(s.pipeName, cfg)
	if err != nil {
		return fmt.Errorf("ipc: listen %s: %w", s.pipeName, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	log.Printf("ipc: listening on %s", s.pipeName)

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return fmt.Errorf("ipc: accept: %w", err)
		}
		s.wg.Add(1)
		go s.serveConn(conn)
	}
}

// serveConn handles one shim connection. The shim keeps the connection
// open for the lifetime of its host process, so this loop runs for minutes
// to hours. A read error means the host closed/exited — log and move on.
func (s *Server) serveConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("ipc: shim connected (%s)", remote)

	for {
		var req Request
		if err := ReadFrame(conn, &req); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				log.Printf("ipc: shim disconnected (%s)", remote)
				return
			}
			log.Printf("ipc: read error from %s: %v", remote, err)
			return
		}

		// Shutdown is special-cased: respond then break so this conn
		// goroutine exits cleanly. The actual listener close happens
		// in Server.Shutdown.
		ctx := context.Background()
		resp, err := s.handler(ctx, req)
		if err != nil {
			resp = Response{Error: err.Error()}
		}
		if err := WriteFrame(conn, &resp); err != nil {
			log.Printf("ipc: write error to %s: %v", remote, err)
			return
		}
		if req.Op == OpShutdown {
			log.Printf("ipc: shutdown requested by %s", remote)
			return
		}
	}
}

// Shutdown stops accepting new connections and waits for in-flight ones
// to finish their current request. Long-running translations happen in
// separate goroutines off the IPC hot path, so this returns quickly.
func (s *Server) Shutdown() error {
	s.mu.Lock()
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		if err := ln.Close(); err != nil {
			return err
		}
	}
	s.wg.Wait()
	return nil
}
