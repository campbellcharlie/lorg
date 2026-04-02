package app

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// Input schema
// ---------------------------------------------------------------------------

type ProtobufArgs struct {
	Action    string `json:"action" jsonschema:"required,enum=decode,decodeHex,decodeTraffic" jsonschema_description:"decode: decode base64 protobuf; decodeHex: decode hex-encoded protobuf; decodeTraffic: decode gRPC response from traffic by request ID"`
	Data      string `json:"data,omitempty" jsonschema_description:"Base64 or hex-encoded protobuf data"`
	RequestID string `json:"requestId,omitempty" jsonschema_description:"Request ID for decodeTraffic action"`
}

// ---------------------------------------------------------------------------
// Wire format decoder (schema-less protobuf decode)
// ---------------------------------------------------------------------------

type protoField struct {
	FieldNumber int    `json:"field"`
	WireType    int    `json:"wireType"`
	TypeName    string `json:"typeName"`
	Value       any    `json:"value"`
}

func decodeProtobuf(data []byte) ([]protoField, error) {
	var fields []protoField
	offset := 0

	for offset < len(data) {
		// Read field tag (varint)
		tag, n := decodeVarint(data[offset:])
		if n <= 0 {
			return fields, fmt.Errorf("invalid varint at offset %d", offset)
		}
		offset += n

		fieldNumber := int(tag >> 3)
		wireType := int(tag & 0x7)

		field := protoField{
			FieldNumber: fieldNumber,
			WireType:    wireType,
		}

		switch wireType {
		case 0: // Varint
			val, vn := decodeVarint(data[offset:])
			if vn <= 0 {
				return fields, fmt.Errorf("invalid varint value at offset %d", offset)
			}
			offset += vn
			field.TypeName = "varint"
			field.Value = val

		case 1: // 64-bit
			if offset+8 > len(data) {
				return fields, fmt.Errorf("truncated 64-bit field at offset %d", offset)
			}
			val := binary.LittleEndian.Uint64(data[offset : offset+8])
			offset += 8
			field.TypeName = "fixed64"
			// Try to interpret as double
			f := math.Float64frombits(val)
			if !math.IsNaN(f) && !math.IsInf(f, 0) && f != 0 && (f > 0.001 || f < -0.001) {
				field.Value = map[string]any{"uint64": val, "double": f}
			} else {
				field.Value = val
			}

		case 2: // Length-delimited (string, bytes, embedded message)
			length, ln := decodeVarint(data[offset:])
			if ln <= 0 {
				return fields, fmt.Errorf("invalid length at offset %d", offset)
			}
			offset += ln
			if offset+int(length) > len(data) {
				return fields, fmt.Errorf("truncated length-delimited field at offset %d", offset)
			}
			payload := data[offset : offset+int(length)]
			offset += int(length)

			// Try to decode as nested message
			nested, err := decodeProtobuf(payload)
			if err == nil && len(nested) > 0 && isLikelyMessage(nested) {
				field.TypeName = "message"
				field.Value = nested
			} else if isValidProtobufUTF8(payload) {
				field.TypeName = "string"
				field.Value = string(payload)
			} else {
				field.TypeName = "bytes"
				field.Value = hex.EncodeToString(payload)
			}

		case 5: // 32-bit
			if offset+4 > len(data) {
				return fields, fmt.Errorf("truncated 32-bit field at offset %d", offset)
			}
			val := binary.LittleEndian.Uint32(data[offset : offset+4])
			offset += 4
			field.TypeName = "fixed32"
			f := math.Float32frombits(val)
			if !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) && f != 0 {
				field.Value = map[string]any{"uint32": val, "float": f}
			} else {
				field.Value = val
			}

		default:
			return fields, fmt.Errorf("unknown wire type %d at offset %d", wireType, offset)
		}

		fields = append(fields, field)
	}

	return fields, nil
}

func decodeVarint(data []byte) (uint64, int) {
	var val uint64
	for i := 0; i < len(data) && i < 10; i++ {
		b := data[i]
		val |= uint64(b&0x7F) << (uint(i) * 7)
		if b < 0x80 {
			return val, i + 1
		}
	}
	return 0, -1
}

// isValidProtobufUTF8 checks whether data looks like printable text.
func isValidProtobufUTF8(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return true
}

// isLikelyMessage is a heuristic: valid field numbers suggest a nested message.
func isLikelyMessage(fields []protoField) bool {
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		if f.FieldNumber <= 0 || f.FieldNumber > 536870911 { // max protobuf field number
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Base64 helpers
// ---------------------------------------------------------------------------

func decodeBase64Protobuf(s string) ([]byte, error) {
	// Try standard base64
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	// Try URL-safe base64
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	// Try without padding
	if decoded, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// ---------------------------------------------------------------------------
// gRPC body extraction
// ---------------------------------------------------------------------------

// extractGRPCBody extracts the protobuf payload from a gRPC HTTP/2 response.
// gRPC frames: 1 byte compressed flag + 4 bytes message length + message.
func extractGRPCBody(raw string) []byte {
	// Find body after header/body separator
	idx := strings.Index(raw, "\r\n\r\n")
	if idx < 0 {
		idx = strings.Index(raw, "\n\n")
		if idx < 0 {
			return nil
		}
		idx += 2
	} else {
		idx += 4
	}

	body := []byte(raw[idx:])
	if len(body) < 5 {
		return body // Not gRPC framed, return raw
	}

	// Check if this looks like a gRPC frame (compressed flag 0 or 1)
	if body[0] <= 1 {
		msgLen := binary.BigEndian.Uint32(body[1:5])
		if int(msgLen) <= len(body)-5 {
			return body[5 : 5+msgLen]
		}
	}

	return body // Return raw body if not gRPC framed
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (backend *Backend) protobufHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args ProtobufArgs
	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	switch args.Action {
	case "decode":
		if args.Data == "" {
			return mcp.NewToolResultError("data is required"), nil
		}
		decoded, err := decodeBase64Protobuf(args.Data)
		if err != nil {
			// Fallback: try as hex
			decoded, err = hex.DecodeString(strings.ReplaceAll(args.Data, " ", ""))
			if err != nil {
				return mcp.NewToolResultError("data must be base64 or hex encoded"), nil
			}
		}
		fields, err := decodeProtobuf(decoded)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("protobuf decode error: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"fields":   fields,
			"count":    len(fields),
			"rawBytes": len(decoded),
		})

	case "decodeHex":
		if args.Data == "" {
			return mcp.NewToolResultError("data is required"), nil
		}
		decoded, err := hex.DecodeString(strings.ReplaceAll(args.Data, " ", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid hex: %v", err)), nil
		}
		fields, err := decodeProtobuf(decoded)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("protobuf decode error: %v", err)), nil
		}
		return mcpJSONResult(map[string]any{
			"fields":   fields,
			"count":    len(fields),
			"rawBytes": len(decoded),
		})

	case "decodeTraffic":
		if args.RequestID == "" {
			return mcp.NewToolResultError("requestId is required for decodeTraffic"), nil
		}
		return backend.protobufDecodeTrafficHandler(args.RequestID)

	default:
		return mcp.NewToolResultError("unknown action: " + args.Action + ". Valid: decode, decodeHex, decodeTraffic"), nil
	}
}

func (backend *Backend) protobufDecodeTrafficHandler(requestID string) (*mcp.CallToolResult, error) {
	if projectDB == nil || projectDB.db == nil {
		return mcp.NewToolResultError("project database not initialized"), nil
	}

	projectDB.mu.Lock()
	defer projectDB.mu.Unlock()

	// Get raw response from _raw table
	var rawResp string
	err := projectDB.db.QueryRow(`SELECT response FROM _raw WHERE id = ?`, requestID).Scan(&rawResp)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("request %s not found: %v", requestID, err)), nil
	}

	// Extract body after headers
	body := extractGRPCBody(rawResp)
	if len(body) == 0 {
		return mcp.NewToolResultError("no body found in response or body is empty"), nil
	}

	fields, err := decodeProtobuf(body)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("protobuf decode error: %v", err)), nil
	}

	return mcpJSONResult(map[string]any{
		"requestId": requestID,
		"fields":    fields,
		"count":     len(fields),
		"rawBytes":  len(body),
	})
}
