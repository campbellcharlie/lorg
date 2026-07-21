package app

import (
	"bufio"
	"io"
	"net"
	"net/http/httputil"
	"net/textproto"
	"strconv"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Desync lab: two servers that genuinely DISAGREE on request framing.
//
//   - honor "TE": reads the body as Transfer-Encoding: chunked, ignoring
//     Content-Length. Blocks on an incomplete chunk. (Models a smuggling
//     backend that honours TE.)
//   - honor "CL": reads exactly Content-Length body bytes, ignoring
//     Transfer-Encoding. Blocks when fewer bytes than promised are on the wire.
//     (Models a component that honours CL.)
//
// The disagreement is real — the same attack bytes make one server hang and the
// other respond. That is exactly the framing confusion CL.TE / TE.CL smuggling
// exploits, and it is what the timeout-differential oracle detects. Nothing here
// encodes the verdict; the servers just parse HTTP, and the oracle infers the
// mismatch from the timing.
// ---------------------------------------------------------------------------

func readRequestHead(br *bufio.Reader) (textproto.MIMEHeader, error) {
	tp := textproto.NewReader(br)
	if _, err := tp.ReadLine(); err != nil { // request line
		return nil, err
	}
	return tp.ReadMIMEHeader()
}

func handleLabConn(conn net.Conn, honor string) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	h, err := readRequestHead(br)
	if err != nil {
		return
	}

	hasTE := h.Get("Transfer-Encoding") != ""
	hasCL := h.Get("Content-Length") != ""

	// Read the body per the framing this server honours. Either read may block
	// forever on a crafted probe — that block IS the vulnerability signal. The
	// client's read deadline (not ours) decides when to give up; when it closes
	// the socket, our blocked Read unblocks with an error and we return.
	readTE := func() { io.ReadAll(httputil.NewChunkedReader(br)) }
	readCL := func() {
		n, _ := strconv.Atoi(h.Get("Content-Length"))
		io.ReadFull(br, make([]byte, n))
	}

	switch {
	case honor == "TE" && hasTE:
		readTE()
	case honor == "CL" && hasCL:
		readCL()
	case hasCL:
		readCL()
	case hasTE:
		readTE()
	}

	conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nok\n"))
}

// startLabServer stands up a framing-honouring HTTP/1.1 server and returns its
// host/port. honor is "TE" or "CL".
func startLabServer(t *testing.T, honor string) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleLabConn(conn, honor)
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return host, port
}

// TestDesyncOracleEndToEnd drives the real MCP desync probe against the lab. The
// 2x2 (technique x which framing the server honours) proves both the positive
// detection and the safe-control discipline: the technique fires ONLY against
// the server whose framing it targets.
func TestDesyncOracleEndToEnd(t *testing.T) {
	teHost, tePort := startLabServer(t, "TE") // honours Transfer-Encoding
	clHost, clPort := startLabServer(t, "CL") // honours Content-Length

	const connectTO = 2 * time.Second
	const readTO = 400 * time.Millisecond // hang threshold — keeps the test fast

	cases := []struct {
		name      string
		technique desyncTechnique
		host      string
		port      int
		wantVuln  bool
	}{
		{"CL.TE detects a TE-honouring server", techCLTE, teHost, tePort, true},
		{"CL.TE is silent on a CL-honouring server (safe control)", techCLTE, clHost, clPort, false},
		{"TE.CL detects a CL-honouring server", techTECL, clHost, clPort, true},
		{"TE.CL is silent on a TE-honouring server (safe control)", techTECL, teHost, tePort, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runDesyncProbe(tc.host, tc.port, false, tc.technique, "/", connectTO, readTO)
			if err != nil {
				t.Fatalf("runDesyncProbe: %v", err)
			}
			if res.Vulnerable != tc.wantVuln {
				t.Errorf("Vulnerable=%v, want %v\n  reason: %s\n  attackTimedOut=%v controlTimedOut=%v (attackMs=%d controlMs=%d)",
					res.Vulnerable, tc.wantVuln, res.Reason,
					res.AttackTimedOut, res.ControlTimedOut, res.AttackMs, res.ControlMs)
			}
			// The control probe must NEVER hang — if it does, the oracle's
			// false-positive guard isn't being exercised and a "vulnerable"
			// result would be meaningless.
			if res.ControlTimedOut {
				t.Errorf("control probe timed out — oracle guard not exercised (reason: %s)", res.Reason)
			}
		})
	}
}

// TestDesyncOracleDecision unit-tests the verdict logic in isolation, including
// the "hangs on everything" case the paired-probe design exists to reject.
func TestDesyncOracleDecision(t *testing.T) {
	to := readResult{timedOut: true}
	ok := readResult{timedOut: false}

	if v, _ := desyncOracle(to, ok); !v {
		t.Error("attack-hang + control-ok must be vulnerable")
	}
	if v, _ := desyncOracle(to, to); v {
		t.Error("both-hang must NOT be vulnerable (server hangs on everything)")
	}
	if v, _ := desyncOracle(ok, ok); v {
		t.Error("neither-hang must NOT be vulnerable")
	}
	if v, _ := desyncOracle(ok, to); v {
		t.Error("attack-ok must NOT be vulnerable regardless of control")
	}
}
