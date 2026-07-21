package app

import (
	"net"
	"testing"
	"time"
)

// TestReadResponseTimed verifies the timeout-differential signal that a desync
// oracle depends on: a server that hangs must surface timedOut=true, while a
// server that answers and closes must surface timedOut=false. Before this,
// readResponse swallowed the deadline error and both cases looked identical.
func TestReadResponseTimed(t *testing.T) {
	t.Run("hanging_read_reports_timeout", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()

		// Accept but never write — forces the reader onto its deadline.
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open past the read timeout, then close.
			time.Sleep(500 * time.Millisecond)
			conn.Close()
		}()

		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()

		res := readResponseTimed(conn, 100*time.Millisecond, 4096)
		if !res.timedOut {
			t.Errorf("expected timedOut=true for a hanging server, got false (read %d bytes)", len(res.data))
		}
		if len(res.data) != 0 {
			t.Errorf("expected no data from a silent server, got %d bytes", len(res.data))
		}
	})

	t.Run("responding_read_reports_no_timeout", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer ln.Close()

		payload := []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi")
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Write(payload)
			conn.Close()
		}()

		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()

		res := readResponseTimed(conn, 2*time.Second, 4096)
		if res.timedOut {
			t.Error("expected timedOut=false for a responding server, got true")
		}
		if string(res.data) != string(payload) {
			t.Errorf("expected full payload, got %q", string(res.data))
		}
	})
}
