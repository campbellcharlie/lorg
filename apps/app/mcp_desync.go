package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// HTTP request-smuggling (desync) probe — timeout-differential oracle
// ---------------------------------------------------------------------------
//
// Detection method (defparam/smuggler): send a PAIR of probes on separate
// connections and compare their read outcomes.
//
//   - attack  — a request whose body is framed one way by Content-Length and
//     another way by Transfer-Encoding, crafted so a server that honours the
//     "wrong" header is left waiting for bytes that never arrive → the read
//     hangs (times out).
//   - control — a well-formed request that completes under either framing →
//     the server responds normally (no timeout).
//
// The pairing is what kills false positives: a server that simply hangs on
// everything fails the oracle because its control probe ALSO times out. A real
// framing disagreement is asserted only when attack times out AND control does
// not. This mirrors smuggler.py's _check_clte / _check_tecl decision.
//
// This consumes the timedOut signal that readResponseTimed surfaces
// (mcp_raw_socket.go) — without it, a hung socket and a normal one are
// indistinguishable and this oracle cannot work.

type desyncTechnique string

const (
	// techCLTE: frontend uses Content-Length, backend uses Transfer-Encoding.
	// A TE-honouring server hangs on an incomplete chunk that CL considers done.
	techCLTE desyncTechnique = "CL.TE"
	// techTECL: frontend uses Transfer-Encoding, backend uses Content-Length.
	// A CL-honouring server hangs waiting for body bytes that TE considers done.
	techTECL desyncTechnique = "TE.CL"
)

// buildDesyncProbes returns the raw attack and control request bytes for a
// technique. Line endings are CRLF as required by HTTP/1.1. Connection:
// keep-alive keeps the socket open so a hung server keeps waiting (rather than
// seeing EOF), which is what lets the read deadline fire.
func buildDesyncProbes(host, path string, technique desyncTechnique) (attack, control []byte) {
	if path == "" {
		path = "/"
	}
	hdr := func(contentLength int, body string) []byte {
		return []byte(fmt.Sprintf(
			"POST %s HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Content-Length: %d\r\n"+
				"Transfer-Encoding: chunked\r\n"+
				"Connection: keep-alive\r\n"+
				"\r\n"+
				"%s",
			path, host, contentLength, body))
	}

	switch technique {
	case techTECL:
		// Attack: TE says the body is complete ("0\r\n\r\n"), but CL promises 6
		// bytes and only 5 are on the wire — a CL-honouring server hangs on the
		// missing byte; a TE-honouring server sees a complete body and replies.
		attack = hdr(6, "0\r\n\r\n")
		// Control: CL matches the bytes present → completes under either framing.
		control = hdr(5, "0\r\n\r\n")
	default: // techCLTE
		// Attack: CL says 4 bytes ("1\r\nA"), which it delivers in full, but that
		// is an INCOMPLETE chunk — a TE-honouring server keeps waiting for the
		// chunk's terminating CRLF and next chunk; a CL-honouring server is done.
		attack = hdr(4, "1\r\nA")
		// Control: a complete, empty chunked body → both framings finish.
		control = hdr(5, "0\r\n\r\n")
	}
	return attack, control
}

// desyncOracle applies the timeout-differential decision to a probe pair.
func desyncOracle(attack, control readResult) (vulnerable bool, reason string) {
	switch {
	case attack.timedOut && !control.timedOut:
		return true, "attack probe hung (read timed out) while the control probe returned normally — the server frames the body differently under Content-Length vs Transfer-Encoding"
	case attack.timedOut && control.timedOut:
		return false, "both probes timed out — server likely hangs on everything (not a framing disagreement); inconclusive"
	case !attack.timedOut:
		return false, "attack probe returned normally — no read-hang differential observed"
	default:
		return false, "no differential observed"
	}
}

type desyncProbeResult struct {
	Technique       string `json:"technique"`
	Vulnerable      bool   `json:"vulnerable"`
	Reason          string `json:"reason"`
	AttackTimedOut  bool   `json:"attackTimedOut"`
	ControlTimedOut bool   `json:"controlTimedOut"`
	AttackMs        int64  `json:"attackMs"`
	ControlMs       int64  `json:"controlMs"`
}

// dialProbe opens one connection (TCP or TLS) and returns it.
func dialProbe(host string, port int, useTLS bool, connectTO time.Duration) (net.Conn, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	if useTLS {
		dialer := &net.Dialer{Timeout: connectTO}
		return tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: true,
			NextProtos:         []string{"http/1.1"},
		})
	}
	return net.DialTimeout("tcp", addr, connectTO)
}

// sendOneProbe sends a single probe on a fresh connection and reports the read
// outcome (including whether it hung).
func sendOneProbe(host string, port int, useTLS bool, payload []byte, connectTO, readTO time.Duration) (readResult, error) {
	conn, err := dialProbe(host, port, useTLS, connectTO)
	if err != nil {
		return readResult{}, err
	}
	defer conn.Close()

	if _, err := conn.Write(payload); err != nil {
		return readResult{}, fmt.Errorf("write probe: %w", err)
	}
	return readResponseTimed(conn, readTO, 8192), nil
}

// runDesyncProbe sends the attack/control pair and applies the oracle.
func runDesyncProbe(host string, port int, useTLS bool, technique desyncTechnique, path string, connectTO, readTO time.Duration) (desyncProbeResult, error) {
	attackReq, controlReq := buildDesyncProbes(host, path, technique)

	attack, err := sendOneProbe(host, port, useTLS, attackReq, connectTO, readTO)
	if err != nil {
		return desyncProbeResult{}, fmt.Errorf("attack probe: %w", err)
	}
	control, err := sendOneProbe(host, port, useTLS, controlReq, connectTO, readTO)
	if err != nil {
		return desyncProbeResult{}, fmt.Errorf("control probe: %w", err)
	}

	vulnerable, reason := desyncOracle(attack, control)
	return desyncProbeResult{
		Technique:       string(technique),
		Vulnerable:      vulnerable,
		Reason:          reason,
		AttackTimedOut:  attack.timedOut,
		ControlTimedOut: control.timedOut,
		AttackMs:        attack.elapsed.Milliseconds(),
		ControlMs:       control.elapsed.Milliseconds(),
	}, nil
}

// ---------------------------------------------------------------------------
// MCP tool
// ---------------------------------------------------------------------------

type DesyncProbeArgs struct {
	Host             string `json:"host" jsonschema:"required" jsonschema_description:"Target hostname"`
	Port             int    `json:"port" jsonschema:"required" jsonschema_description:"Target port"`
	UseTLS           bool   `json:"useTls,omitempty" jsonschema_description:"Use TLS (https). Default false"`
	Technique        string `json:"technique,omitempty" jsonschema_description:"CL.TE or TE.CL (default CL.TE)"`
	Path             string `json:"path,omitempty" jsonschema_description:"Request path (default /)"`
	ConnectTimeoutMs int    `json:"connectTimeoutMs,omitempty" jsonschema_description:"Connect timeout in ms (default 5000)"`
	ReadTimeoutMs    int    `json:"readTimeoutMs,omitempty" jsonschema_description:"Read timeout in ms — the hang threshold (default 5000)"`
}

func (backend *Backend) desyncProbeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args DesyncProbeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	technique := techCLTE
	if desyncTechnique(args.Technique) == techTECL {
		technique = techTECL
	}
	connectTO := time.Duration(intOrDefault(args.ConnectTimeoutMs, 5000)) * time.Millisecond
	readTO := time.Duration(intOrDefault(args.ReadTimeoutMs, 5000)) * time.Millisecond

	result, err := runDesyncProbe(args.Host, args.Port, args.UseTLS, technique, args.Path, connectTO, readTO)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcpJSONResult(result)
}

func intOrDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
