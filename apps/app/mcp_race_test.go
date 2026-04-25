package app

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// httpVersionTestServer accepts a single TCP connection, reads the request,
// and writes back the response written verbatim.
//
// It does NOT close the connection — this is the key behavior that exposes
// the bug: HTTP/1.0 servers with explicit Content-Length may keep the socket
// open (waiting for another request or for the client to close), so a reader
// that waits for connection close hangs.
type httpVersionTestServer struct {
	listener net.Listener
	response string
	keepOpen bool
}

func newHTTPVersionTestServer(t *testing.T, response string, keepOpen bool) *httpVersionTestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &httpVersionTestServer{listener: ln, response: response, keepOpen: keepOpen}
	go srv.serve()
	return srv
}

func (s *httpVersionTestServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			// Read request line + headers (until blank line).
			r := bufio.NewReader(c)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					c.Close()
					return
				}
				if line == "\r\n" || line == "\n" {
					break
				}
			}
			c.Write([]byte(s.response))
			if !s.keepOpen {
				c.Close()
			}
			// If keepOpen: deliberately leave socket open. Caller cleanup
			// happens when the listener is closed.
		}(conn)
	}
}

func (s *httpVersionTestServer) Close() { s.listener.Close() }

func (s *httpVersionTestServer) HostPort() (string, int) {
	addr := s.listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func dialAndSend(t *testing.T, host string, port int, request string, timeout time.Duration) (status string, n int, elapsed time.Duration, err error) {
	t.Helper()
	conn, derr := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 5*time.Second)
	if derr != nil {
		err = derr
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	start := time.Now()
	status, n, err = sendAndRead(conn, []byte(request))
	elapsed = time.Since(start)
	return
}

// TestSendAndRead_HTTP10_ContentLength_KeepOpen pins the bug. An HTTP/1.0
// response with Content-Length and no Connection: close, on a server that
// keeps the socket open, must not block waiting for connection close.
func TestSendAndRead_HTTP10_ContentLength_KeepOpen(t *testing.T) {
	body := "hello world"
	resp := strings.Join([]string{
		"HTTP/1.0 200 OK",
		"Content-Type: text/plain",
		fmt.Sprintf("Content-Length: %d", len(body)),
		"",
		body,
	}, "\r\n")

	srv := newHTTPVersionTestServer(t, resp, true)
	defer srv.Close()

	host, port := srv.HostPort()
	req := "GET / HTTP/1.0\r\nHost: x\r\n\r\n"

	status, n, elapsed, err := dialAndSend(t, host, port, req, 3*time.Second)
	if err != nil {
		t.Fatalf("sendAndRead error: %v", err)
	}
	if !strings.HasPrefix(status, "HTTP/1.0 200") {
		t.Errorf("unexpected status line: %q", status)
	}
	if n == 0 {
		t.Errorf("expected non-zero body length")
	}
	if elapsed > 1*time.Second {
		t.Errorf("HTTP/1.0 read should return promptly when Content-Length is satisfied, took %v", elapsed)
	}
}

// TestSendAndRead_HTTP11_KeepAlive verifies the same fix works for HTTP/1.1
// keep-alive responses (Connection: keep-alive is the default in 1.1).
func TestSendAndRead_HTTP11_KeepAlive(t *testing.T) {
	body := `{"ok":true}`
	resp := strings.Join([]string{
		"HTTP/1.1 200 OK",
		"Content-Type: application/json",
		fmt.Sprintf("Content-Length: %d", len(body)),
		"",
		body,
	}, "\r\n")

	srv := newHTTPVersionTestServer(t, resp, true)
	defer srv.Close()

	host, port := srv.HostPort()
	req := "GET / HTTP/1.1\r\nHost: x\r\n\r\n"

	status, n, elapsed, err := dialAndSend(t, host, port, req, 3*time.Second)
	if err != nil {
		t.Fatalf("sendAndRead error: %v", err)
	}
	if !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Errorf("unexpected status line: %q", status)
	}
	if n == 0 {
		t.Errorf("expected non-zero body length")
	}
	if elapsed > 1*time.Second {
		t.Errorf("HTTP/1.1 keep-alive read should return promptly, took %v", elapsed)
	}
}

// TestSendAndRead_Chunked verifies chunked transfer-encoding is read to
// completion (terminated by 0-length chunk) without waiting for socket close.
func TestSendAndRead_Chunked(t *testing.T) {
	resp := strings.Join([]string{
		"HTTP/1.1 200 OK",
		"Content-Type: text/plain",
		"Transfer-Encoding: chunked",
		"",
		"5",
		"hello",
		"6",
		" world",
		"0",
		"",
		"",
	}, "\r\n")

	srv := newHTTPVersionTestServer(t, resp, true)
	defer srv.Close()

	host, port := srv.HostPort()
	req := "GET / HTTP/1.1\r\nHost: x\r\n\r\n"

	status, n, elapsed, err := dialAndSend(t, host, port, req, 3*time.Second)
	if err != nil {
		t.Fatalf("sendAndRead error: %v", err)
	}
	if !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Errorf("unexpected status line: %q", status)
	}
	if n == 0 {
		t.Errorf("expected non-zero body length")
	}
	if elapsed > 1*time.Second {
		t.Errorf("chunked read should return promptly after 0-chunk, took %v", elapsed)
	}
}

// TestSendAndRead_HTTP10_CloseDelimited preserves the legacy behavior: an
// HTTP/1.0 response without Content-Length signals message end via close.
func TestSendAndRead_HTTP10_CloseDelimited(t *testing.T) {
	body := "no length here"
	resp := strings.Join([]string{
		"HTTP/1.0 200 OK",
		"Content-Type: text/plain",
		"",
		body,
	}, "\r\n")

	srv := newHTTPVersionTestServer(t, resp, false) // server closes after writing
	defer srv.Close()

	host, port := srv.HostPort()
	req := "GET / HTTP/1.0\r\nHost: x\r\n\r\n"

	status, n, _, err := dialAndSend(t, host, port, req, 3*time.Second)
	if err != nil {
		t.Fatalf("sendAndRead error: %v", err)
	}
	if !strings.HasPrefix(status, "HTTP/1.0 200") {
		t.Errorf("unexpected status line: %q", status)
	}
	if n == 0 {
		t.Errorf("expected non-zero body length")
	}
}
