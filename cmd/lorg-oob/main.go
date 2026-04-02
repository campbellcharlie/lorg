package main

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/xid"
)

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

// Interaction represents a single OOB callback event (HTTP or DNS).
type Interaction struct {
	ID        string            `json:"id"`
	Token     string            `json:"token"`
	Type      string            `json:"type"` // "http", "dns"
	SourceIP  string            `json:"source_ip"`
	Timestamp time.Time         `json:"timestamp"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	DNSQuery  string            `json:"dns_query,omitempty"`
	DNSType   string            `json:"dns_type,omitempty"`
}

// Store is a thread-safe, bounded in-memory ring buffer for interactions.
type Store struct {
	mu           sync.RWMutex
	interactions []Interaction
	maxSize      int
}

// NewStore creates a Store that retains at most maxSize interactions.
func NewStore(maxSize int) *Store {
	return &Store{
		interactions: make([]Interaction, 0, 256),
		maxSize:      maxSize,
	}
}

// Add appends an interaction, assigning it a unique ID. When the buffer is
// full the oldest entries are discarded.
func (s *Store) Add(i Interaction) {
	i.ID = xid.New().String()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interactions = append(s.interactions, i)
	// Ring buffer: drop oldest when full
	if len(s.interactions) > s.maxSize {
		s.interactions = s.interactions[len(s.interactions)-s.maxSize:]
	}
}

// Query returns interactions matching the optional token and since filters,
// up to limit results.
func (s *Store) Query(token string, since time.Time, limit int) []Interaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []Interaction
	for _, i := range s.interactions {
		if token != "" && i.Token != token {
			continue
		}
		if !since.IsZero() && i.Timestamp.Before(since) {
			continue
		}
		result = append(result, i)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

// Clear removes all interactions and returns how many were removed.
func (s *Store) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.interactions)
	s.interactions = s.interactions[:0]
	return n
}

// Count returns the current number of stored interactions.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.interactions)
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

var (
	apiAddr         string
	httpAddr        string
	dnsAddr         string
	apiToken        string
	dnsResponseIP   string
	domain          string
	maxInteractions int
	tlsCert         string
	tlsKey          string
)

var store *Store

var startTime = time.Now()

func main() {
	flag.StringVar(&apiAddr, "api", ":8443", "API listen address (for lorg to poll)")
	flag.StringVar(&httpAddr, "http", ":80", "HTTP callback trap address")
	flag.StringVar(&dnsAddr, "dns", ":53", "DNS callback trap address (empty to disable)")
	flag.StringVar(&apiToken, "token", "", "API bearer token (auto-generated if empty)")
	flag.StringVar(&dnsResponseIP, "dns-ip", "127.0.0.1", "IP to respond with for DNS A queries")
	flag.StringVar(&domain, "domain", "", "Domain name for DNS callbacks (e.g. oob.yourdomain.com)")
	flag.IntVar(&maxInteractions, "max", 10000, "Max stored interactions (ring buffer)")
	flag.StringVar(&tlsCert, "tls-cert", "", "TLS certificate file for API server (enables HTTPS)")
	flag.StringVar(&tlsKey, "tls-key", "", "TLS private key file for API server")
	flag.Parse()

	// Auto-generate token if not provided
	if apiToken == "" {
		tokenBytes := make([]byte, 24)
		if _, err := rand.Read(tokenBytes); err != nil {
			log.Fatal("failed to generate token:", err)
		}
		apiToken = hex.EncodeToString(tokenBytes)
	}

	store = NewStore(maxInteractions)

	log.Println("========================================")
	log.Println("  lorg-oob - Out-of-Band Callback Server")
	log.Println("========================================")
	log.Printf("  API token:    %s", apiToken)
	log.Printf("  API address:  %s", apiAddr)
	log.Printf("  HTTP trap:    %s", httpAddr)
	if dnsAddr != "" {
		log.Printf("  DNS trap:     %s", dnsAddr)
		log.Printf("  DNS response: %s", dnsResponseIP)
		if domain != "" {
			log.Printf("  Domain:       %s", domain)
		}
	} else {
		log.Println("  DNS trap:     disabled")
	}
	log.Printf("  Max stored:   %d", maxInteractions)
	if tlsCert != "" {
		log.Printf("  TLS:          enabled (cert=%s)", tlsCert)
	} else {
		log.Println("  TLS:          disabled (API is plain HTTP)")
	}
	log.Println("========================================")

	// Start HTTP callback trap
	go startHTTPTrap()

	// Start DNS callback trap
	if dnsAddr != "" {
		go startDNSTrap()
	}

	// Start API server (blocking)
	startAPIServer()
}

// ---------------------------------------------------------------------------
// HTTP Callback Trap
// ---------------------------------------------------------------------------

func startHTTPTrap() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpTrapHandler)

	server := &http.Server{
		Addr:         httpAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("[HTTP] Trap listening on %s", httpAddr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("[HTTP] Trap server error: %v", err)
	}
}

// 1x1 transparent GIF
var transparentGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00,
	0x01, 0x00, 0x80, 0x00, 0x00, 0xff, 0xff, 0xff,
	0x00, 0x00, 0x00, 0x21, 0xf9, 0x04, 0x01, 0x00,
	0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
	0x01, 0x00, 0x3b,
}

func httpTrapHandler(w http.ResponseWriter, r *http.Request) {
	// Extract token from first path segment
	path := strings.TrimPrefix(r.URL.Path, "/")
	token := path
	if idx := strings.IndexByte(token, '/'); idx >= 0 {
		token = token[:idx]
	}

	// Capture headers
	headers := make(map[string]string)
	for k, v := range r.Header {
		headers[k] = strings.Join(v, ", ")
	}

	// Read body (cap at 32KB)
	var body string
	if r.Body != nil {
		buf := make([]byte, 32768)
		n, _ := r.Body.Read(buf)
		body = string(buf[:n])
		r.Body.Close()
	}

	sourceIP := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		sourceIP = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}

	interaction := Interaction{
		Token:     token,
		Type:      "http",
		SourceIP:  sourceIP,
		Timestamp: time.Now().UTC(),
		Method:    r.Method,
		Path:      r.URL.Path,
		Headers:   headers,
		Body:      body,
	}
	store.Add(interaction)

	log.Printf("[HTTP] token=%s src=%s %s %s", token, sourceIP, r.Method, r.URL.Path)

	// Respond: 1x1 transparent GIF with permissive CORS
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(transparentGIF)
}

// ---------------------------------------------------------------------------
// DNS Callback Trap
// ---------------------------------------------------------------------------

func startDNSTrap() {
	addr, err := net.ResolveUDPAddr("udp", dnsAddr)
	if err != nil {
		log.Fatalf("[DNS] Resolve error: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("[DNS] Listen error: %v", err)
	}
	defer conn.Close()

	log.Printf("[DNS] Trap listening on %s", dnsAddr)

	buf := make([]byte, 512)
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("[DNS] Read error: %v", err)
			continue
		}
		if n < 12 {
			continue
		}

		queryName, qtype := parseDNSQuery(buf[12:n])

		// Token = first label
		token := queryName
		if idx := strings.IndexByte(token, '.'); idx >= 0 {
			token = token[:idx]
		}

		interaction := Interaction{
			Token:     token,
			Type:      "dns",
			SourceIP:  raddr.String(),
			Timestamp: time.Now().UTC(),
			DNSQuery:  queryName,
			DNSType:   qtype,
		}
		store.Add(interaction)

		log.Printf("[DNS] token=%s src=%s query=%s type=%s", token, raddr.String(), queryName, qtype)

		// Build and send response
		resp := buildDNSResp(buf[:n], dnsResponseIP)
		if resp != nil {
			conn.WriteToUDP(resp, raddr)
		}
	}
}

func parseDNSQuery(data []byte) (string, string) {
	// Parse query name from DNS wire format
	var parts []string
	i := 0
	for i < len(data) {
		length := int(data[i])
		if length == 0 {
			i++
			break
		}
		i++
		if i+length > len(data) {
			break
		}
		parts = append(parts, string(data[i:i+length]))
		i += length
	}
	name := strings.Join(parts, ".")

	// Parse query type (2 bytes after name null terminator)
	qtype := "A"
	if i+2 <= len(data) {
		qt := int(data[i])<<8 | int(data[i+1])
		switch qt {
		case 1:
			qtype = "A"
		case 2:
			qtype = "NS"
		case 5:
			qtype = "CNAME"
		case 15:
			qtype = "MX"
		case 16:
			qtype = "TXT"
		case 28:
			qtype = "AAAA"
		default:
			qtype = fmt.Sprintf("TYPE%d", qt)
		}
	}
	return name, qtype
}

func buildDNSResp(query []byte, responseIP string) []byte {
	if len(query) < 12 {
		return nil
	}

	ip := net.ParseIP(responseIP).To4()
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1).To4()
	}

	resp := make([]byte, len(query))
	copy(resp, query)

	// QR=1, AA=1, RD=1
	resp[2] = 0x85
	resp[3] = 0x80

	// ANCOUNT=1
	resp[6] = 0x00
	resp[7] = 0x01

	// Answer: pointer to name + A record + TTL 60 + IP
	answer := []byte{
		0xc0, 0x0c, // Name pointer
		0x00, 0x01, // Type A
		0x00, 0x01, // Class IN
		0x00, 0x00, 0x00, 0x3c, // TTL 60
		0x00, 0x04, // RDLENGTH
	}
	answer = append(answer, ip...)
	resp = append(resp, answer...)

	return resp
}

// ---------------------------------------------------------------------------
// API Server (for lorg to poll)
// ---------------------------------------------------------------------------

func startAPIServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/interactions", apiAuth(apiInteractionsHandler))
	mux.HandleFunc("/api/interactions/clear", apiAuth(apiClearHandler))
	mux.HandleFunc("/api/health", apiHealthHandler)
	mux.HandleFunc("/api/token/new", apiAuth(apiNewTokenHandler))

	server := &http.Server{
		Addr:         apiAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	if tlsCert != "" && tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			log.Fatalf("[API] Failed to load TLS cert/key: %v", err)
		}
		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		log.Printf("[API] Listening on %s (TLS)", apiAddr)
		if err := server.ListenAndServeTLS("", ""); err != nil {
			log.Fatalf("[API] Server error: %v", err)
		}
	} else {
		log.Printf("[API] Listening on %s", apiAddr)
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("[API] Server error: %v", err)
		}
	}
}

func apiAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(apiToken)) != 1 {
			http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func apiHealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"interactions": store.Count(),
		"uptime":       time.Since(startTime).String(),
	})
}

func apiInteractionsHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")

	var since time.Time
	if sinceStr != "" {
		since, _ = time.Parse(time.RFC3339, sinceStr)
	}

	limit := 100
	if limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
	}

	results := store.Query(token, since, limit)
	if results == nil {
		results = []Interaction{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"interactions": results,
		"count":        len(results),
		"total":        store.Count(),
	})
}

func apiClearHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, `{"error":"use POST or DELETE"}`, http.StatusMethodNotAllowed)
		return
	}
	n := store.Clear()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"cleared": n,
		"success": true,
	})
}

func apiNewTokenHandler(w http.ResponseWriter, r *http.Request) {
	token := xid.New().String()

	// Build payload URLs
	result := map[string]any{
		"token": token,
	}

	// Determine external host from request or flags
	host := r.URL.Query().Get("host")
	if host == "" && domain != "" {
		host = domain
	}
	if host == "" {
		host = r.Host
		// Strip port from API host
		if idx := strings.LastIndexByte(host, ':'); idx >= 0 {
			host = host[:idx]
		}
	}

	// HTTP payloads — append trap port only if host doesn't already include one
	httpPort := ""
	if !strings.Contains(host, ":") && httpAddr != ":80" {
		parts := strings.Split(httpAddr, ":")
		if len(parts) > 1 && parts[len(parts)-1] != "80" {
			httpPort = ":" + parts[len(parts)-1]
		}
	}
	httpURL := fmt.Sprintf("http://%s%s/%s", host, httpPort, token)
	result["http_url"] = httpURL
	result["payloads"] = map[string]string{
		"url":     httpURL,
		"img":     fmt.Sprintf(`<img src="%s">`, httpURL),
		"fetch":   fmt.Sprintf(`fetch('%s')`, httpURL),
		"curl":    fmt.Sprintf(`curl %s`, httpURL),
		"script":  fmt.Sprintf(`<script src="%s"></script>`, httpURL),
		"css":     fmt.Sprintf(`<link rel="stylesheet" href="%s">`, httpURL),
		"iframe":  fmt.Sprintf(`<iframe src="%s"></iframe>`, httpURL),
		"xmlhttp": fmt.Sprintf(`var x=new XMLHttpRequest();x.open('GET','%s');x.send();`, httpURL),
	}

	// DNS payloads (if domain is set)
	if domain != "" {
		dnsName := fmt.Sprintf("%s.%s", token, domain)
		result["dns_name"] = dnsName
		result["payloads"].(map[string]string)["nslookup"] = fmt.Sprintf("nslookup %s", dnsName)
		result["payloads"].(map[string]string)["dig"] = fmt.Sprintf("dig %s", dnsName)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
