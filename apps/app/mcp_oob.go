package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/xid"
)

// oobHTTPClient is used for remote OOB API calls. It skips TLS verification
// because the remote server uses a Cloudflare Origin CA cert that isn't in
// the system trust store. Authentication is handled by bearer token.
var oobHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

// ---------------------------------------------------------------------------
// OOB Callback Server
// ---------------------------------------------------------------------------

type oobInteraction struct {
	Token     string            `json:"token"`
	Type      string            `json:"type"` // "http" or "dns"
	SourceIP  string            `json:"source_ip"`
	Timestamp time.Time         `json:"timestamp"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Query     string            `json:"query,omitempty"` // DNS query name
}

type oobServer struct {
	mu            sync.Mutex
	httpListener  net.Listener
	httpServer    *http.Server
	dnsConn       *net.UDPConn
	interactions  []oobInteraction
	tokens        map[string]time.Time // token -> created timestamp
	running       bool
	host          string
	httpPort      int
	dnsPort       int
	dnsResponseIP string

	// Remote mode
	remoteURL   string // e.g. "http://vps.example.com:8443"
	remoteToken string // API bearer token for remote server
	isRemote    bool
}

// oob is the package-level out-of-band interaction server.
// Package-level because OOB handlers access it without Backend reference.
// Thread-safe: oobServer uses an internal mutex for token map operations.
var oob = &oobServer{
	tokens:        make(map[string]time.Time),
	dnsResponseIP: "127.0.0.1",
}

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type OOBArgs struct {
	Action        string `json:"action" jsonschema:"required,enum=start,stop,connectRemote,generatePayload,pollInteractions,clearInteractions" jsonschema_description:"start/stop the callback server, connect to remote lorg-oob server, generate payload URLs, or poll for interactions"`
	Host          string `json:"host,omitempty" jsonschema_description:"External hostname/IP for payloads (e.g. your-vps.com)"`
	HTTPPort      int    `json:"httpPort,omitempty" jsonschema_description:"HTTP listener port (default 9999)"`
	DNSPort       int    `json:"dnsPort,omitempty" jsonschema_description:"DNS listener port (default 0 disabled, set >0 to enable)"`
	DNSResponseIP string `json:"dnsResponseIP,omitempty" jsonschema_description:"IP to respond with for DNS A queries (default 127.0.0.1)"`
	Token         string `json:"token,omitempty" jsonschema_description:"Filter interactions by token"`
	RemoteURL     string `json:"remoteUrl,omitempty" jsonschema_description:"URL of remote lorg-oob server (e.g. http://vps:8443)"`
	RemoteToken   string `json:"remoteToken,omitempty" jsonschema_description:"API bearer token for remote lorg-oob server"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) oobHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args OOBArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "start":
		return backend.oobStartHandler(args)
	case "stop":
		return backend.oobStopHandler()
	case "connectRemote":
		return backend.oobConnectRemoteHandler(args)
	case "generatePayload":
		return backend.oobGeneratePayloadHandler(args)
	case "pollInteractions":
		return backend.oobPollHandler(args)
	case "clearInteractions":
		return backend.oobClearHandler()
	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: start, stop, connectRemote, generatePayload, pollInteractions, clearInteractions"), nil
	}
}

func (backend *Backend) oobStartHandler(args OOBArgs) (*mcp.CallToolResult, error) {
	oob.mu.Lock()
	defer oob.mu.Unlock()

	if oob.running {
		return mcp.NewToolResultError("OOB server already running"), nil
	}

	httpPort := args.HTTPPort
	if httpPort == 0 {
		httpPort = 9999
	}
	dnsPort := args.DNSPort
	// Default DNS port to 0 (disabled) to avoid privilege issues
	host := args.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if args.DNSResponseIP != "" {
		oob.dnsResponseIP = args.DNSResponseIP
	}

	oob.host = host
	oob.httpPort = httpPort
	oob.dnsPort = dnsPort

	// Start HTTP listener
	mux := http.NewServeMux()
	mux.HandleFunc("/", oobHTTPHandler)

	addr := fmt.Sprintf(":%d", httpPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to start HTTP listener on %s: %v", addr, err)), nil
	}
	oob.httpListener = listener

	oob.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		if err := oob.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[OOB] HTTP server error: %v", err)
		}
	}()

	log.Printf("[OOB] HTTP callback server started on :%d", httpPort)

	// Start DNS listener (if enabled and port > 0)
	if dnsPort > 0 {
		dnsAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", dnsPort))
		if err == nil {
			conn, err := net.ListenUDP("udp", dnsAddr)
			if err != nil {
				log.Printf("[OOB] DNS listener failed (continuing without DNS): %v", err)
			} else {
				oob.dnsConn = conn
				go oobDNSHandler(conn)
				log.Printf("[OOB] DNS callback server started on :%d", dnsPort)
			}
		}
	}

	oob.running = true

	return mcpJSONResult(map[string]any{
		"success":  true,
		"httpPort": httpPort,
		"dnsPort":  dnsPort,
		"host":     host,
		"baseURL":  fmt.Sprintf("http://%s:%d", host, httpPort),
	})
}

func (backend *Backend) oobStopHandler() (*mcp.CallToolResult, error) {
	oob.mu.Lock()
	defer oob.mu.Unlock()

	if !oob.running {
		return mcp.NewToolResultError("OOB server not running"), nil
	}

	// Remote mode: just disconnect, don't stop the VPS server
	if oob.isRemote {
		oob.isRemote = false
		oob.remoteURL = ""
		oob.remoteToken = ""
		oob.running = false
		return mcpJSONResult(map[string]any{
			"success": true,
			"message": "Disconnected from remote OOB server",
		})
	}

	if oob.httpServer != nil {
		oob.httpServer.Close()
	}
	if oob.dnsConn != nil {
		oob.dnsConn.Close()
	}
	oob.running = false
	log.Println("[OOB] Callback server stopped")

	return mcpJSONResult(map[string]any{
		"success": true,
		"message": "OOB callback server stopped",
	})
}

func (backend *Backend) oobConnectRemoteHandler(args OOBArgs) (*mcp.CallToolResult, error) {
	if args.RemoteURL == "" {
		return mcp.NewToolResultError("remoteUrl is required"), nil
	}
	if args.RemoteToken == "" {
		return mcp.NewToolResultError("remoteToken is required"), nil
	}

	// Verify connectivity by hitting the health endpoint
	url := strings.TrimRight(args.RemoteURL, "/") + "/api/health"
	resp, err := oobHTTPClient.Get(url)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("cannot reach remote OOB server: %v", err)), nil
	}
	defer resp.Body.Close()

	var health map[string]any
	json.NewDecoder(resp.Body).Decode(&health)

	oob.mu.Lock()
	oob.remoteURL = strings.TrimRight(args.RemoteURL, "/")
	oob.remoteToken = args.RemoteToken
	oob.isRemote = true
	oob.running = true
	oob.mu.Unlock()

	return mcpJSONResult(map[string]any{
		"success": true,
		"mode":    "remote",
		"url":     args.RemoteURL,
		"health":  health,
	})
}

func (backend *Backend) oobGeneratePayloadHandler(args OOBArgs) (*mcp.CallToolResult, error) {
	oob.mu.Lock()
	isRemote := oob.isRemote
	remoteURL := oob.remoteURL
	remoteToken := oob.remoteToken
	host := oob.host
	httpPort := oob.httpPort
	running := oob.running
	oob.mu.Unlock()

	if !running {
		return mcp.NewToolResultError("OOB server not running. Use start (local) or connectRemote (VPS)"), nil
	}

	if isRemote {
		// Call remote server to generate a new token/payload
		url := remoteURL + "/api/token/new"
		if args.Host != "" {
			url += "?host=" + args.Host
		}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+remoteToken)
		resp, err := oobHTTPClient.Do(req)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remote server error: %v", err)), nil
		}
		defer resp.Body.Close()
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		result["mode"] = "remote"
		return mcpJSONResult(result)
	}

	token := xid.New().String()

	oob.mu.Lock()
	oob.tokens[token] = time.Now()
	oob.mu.Unlock()

	httpURL := fmt.Sprintf("http://%s:%d/%s", host, httpPort, token)
	imgTag := fmt.Sprintf(`<img src="%s">`, httpURL)
	fetchJS := fmt.Sprintf(`fetch('%s')`, httpURL)

	result := map[string]any{
		"token":   token,
		"httpURL": httpURL,
		"payloads": map[string]string{
			"url":   httpURL,
			"img":   imgTag,
			"fetch": fetchJS,
			"curl":  fmt.Sprintf("curl %s", httpURL),
		},
	}

	if oob.dnsPort > 0 {
		dnsPayload := fmt.Sprintf("%s.%s", token, host)
		result["dnsPayload"] = dnsPayload
		result["payloads"].(map[string]string)["nslookup"] = fmt.Sprintf("nslookup %s %s -port=%d", dnsPayload, host, oob.dnsPort)
	}

	return mcpJSONResult(result)
}

func (backend *Backend) oobPollHandler(args OOBArgs) (*mcp.CallToolResult, error) {
	oob.mu.Lock()
	isRemote := oob.isRemote
	remoteURL := oob.remoteURL
	remoteToken := oob.remoteToken
	oob.mu.Unlock()

	if isRemote {
		url := remoteURL + "/api/interactions"
		if args.Token != "" {
			url += "?token=" + args.Token
		}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+remoteToken)
		resp, err := oobHTTPClient.Do(req)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remote poll error: %v", err)), nil
		}
		defer resp.Body.Close()
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		result["mode"] = "remote"
		return mcpJSONResult(result)
	}

	oob.mu.Lock()
	defer oob.mu.Unlock()

	var filtered []oobInteraction
	for _, i := range oob.interactions {
		if args.Token == "" || i.Token == args.Token {
			filtered = append(filtered, i)
		}
	}

	return mcpJSONResult(map[string]any{
		"interactions": filtered,
		"count":        len(filtered),
		"filter":       args.Token,
	})
}

func (backend *Backend) oobClearHandler() (*mcp.CallToolResult, error) {
	oob.mu.Lock()
	isRemote := oob.isRemote
	remoteURL := oob.remoteURL
	remoteToken := oob.remoteToken
	oob.mu.Unlock()

	if isRemote {
		url := remoteURL + "/api/interactions/clear"
		req, _ := http.NewRequest("POST", url, nil)
		req.Header.Set("Authorization", "Bearer "+remoteToken)
		resp, err := oobHTTPClient.Do(req)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("remote clear error: %v", err)), nil
		}
		defer resp.Body.Close()
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result)
		result["mode"] = "remote"
		return mcpJSONResult(result)
	}

	oob.mu.Lock()
	count := len(oob.interactions)
	oob.interactions = nil
	oob.mu.Unlock()

	return mcpJSONResult(map[string]any{
		"success": true,
		"cleared": count,
	})
}

// ---------------------------------------------------------------------------
// HTTP handler (runs on the OOB server)
// ---------------------------------------------------------------------------

func oobHTTPHandler(w http.ResponseWriter, r *http.Request) {
	// Extract token from path (first segment after /)
	path := strings.TrimPrefix(r.URL.Path, "/")
	token := path
	if idx := strings.IndexByte(token, '/'); idx >= 0 {
		token = token[:idx]
	}

	// Record interaction
	headers := make(map[string]string)
	for k, v := range r.Header {
		headers[k] = strings.Join(v, ", ")
	}

	// Read body (limit to 10KB)
	var body string
	if r.Body != nil {
		buf := make([]byte, 10240)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
	}

	sourceIP := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		sourceIP = fwd
	}

	interaction := oobInteraction{
		Token:     token,
		Type:      "http",
		SourceIP:  sourceIP,
		Timestamp: time.Now(),
		Method:    r.Method,
		Path:      r.URL.Path,
		Headers:   headers,
		Body:      body,
	}

	oob.mu.Lock()
	oob.interactions = append(oob.interactions, interaction)
	oob.mu.Unlock()

	log.Printf("[OOB] HTTP interaction: token=%s src=%s method=%s path=%s", token, sourceIP, r.Method, r.URL.Path)

	// Respond with 200 OK and a small transparent GIF (for img tags)
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	// 1x1 transparent GIF
	w.Write([]byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00,
		0x01, 0x00, 0x80, 0x00, 0x00, 0xff, 0xff, 0xff,
		0x00, 0x00, 0x00, 0x21, 0xf9, 0x04, 0x01, 0x00,
		0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
		0x01, 0x00, 0x3b,
	})
}

// ---------------------------------------------------------------------------
// Minimal DNS handler (UDP, no miekg/dns dependency)
// TODO: Replace with miekg/dns for proper DNS parsing and response building.
// ---------------------------------------------------------------------------

func oobDNSHandler(conn *net.UDPConn) {
	buf := make([]byte, 512)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if strings.Contains(err.Error(), "closed") {
				return
			}
			log.Printf("[OOB] DNS read error: %v", err)
			continue
		}

		if n < 12 {
			continue
		}

		// Parse DNS query name from the wire format (minimal parsing)
		queryName := parseDNSQueryName(buf[12:n])

		// Extract token (first label before first dot)
		token := queryName
		if idx := strings.IndexByte(token, '.'); idx >= 0 {
			token = token[:idx]
		}

		interaction := oobInteraction{
			Token:     token,
			Type:      "dns",
			SourceIP:  addr.String(),
			Timestamp: time.Now(),
			Query:     queryName,
		}

		oob.mu.Lock()
		oob.interactions = append(oob.interactions, interaction)
		oob.mu.Unlock()

		log.Printf("[OOB] DNS interaction: token=%s src=%s query=%s", token, addr.String(), queryName)

		// Build minimal DNS response
		resp := buildDNSResponse(buf[:n], oob.dnsResponseIP)
		if resp != nil {
			conn.WriteToUDP(resp, addr)
		}
	}
}

func parseDNSQueryName(data []byte) string {
	var parts []string
	i := 0
	for i < len(data) {
		length := int(data[i])
		if length == 0 {
			break
		}
		i++
		if i+length > len(data) {
			break
		}
		parts = append(parts, string(data[i:i+length]))
		i += length
	}
	return strings.Join(parts, ".")
}

func buildDNSResponse(query []byte, responseIP string) []byte {
	if len(query) < 12 {
		return nil
	}

	// Parse the response IP
	ip := net.ParseIP(responseIP).To4()
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1).To4()
	}

	// Copy query as base for response
	resp := make([]byte, len(query))
	copy(resp, query)

	// Set response flags: QR=1, AA=1, RCODE=0
	resp[2] = 0x85 // QR=1, Opcode=0, AA=1, TC=0, RD=1
	resp[3] = 0x80 // RA=1, Z=0, RCODE=0

	// Set answer count to 1
	resp[6] = 0x00
	resp[7] = 0x01

	// Append answer: pointer to query name + A record
	answer := []byte{
		0xc0, 0x0c, // Pointer to name in question section
		0x00, 0x01, // Type A
		0x00, 0x01, // Class IN
		0x00, 0x00, 0x00, 0x3c, // TTL 60
		0x00, 0x04, // RDLENGTH 4
	}
	answer = append(answer, ip...)
	resp = append(resp, answer...)

	return resp
}
