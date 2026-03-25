# Grroxy Tools API Documentation

## Overview

The Grroxy Tools application provides advanced fuzzing capabilities for web application security testing. It supports multiple fuzzing modes, wordlist-based payload generation, and concurrent request execution with both HTTP/1.1 and HTTP/2 support.

**Base URL**: `http://{host}:{port}/api`

---

## Table of Contents

- [Fuzzer](#fuzzer)
  - [Start Fuzzer](#start-fuzzer)
  - [Stop Fuzzer](#stop-fuzzer)
- [Commands](#commands)
- [Data Storage](#data-storage)
- [Error Responses](#error-responses)

---

## Fuzzer

The Fuzzer allows you to perform automated web application fuzzing with customizable payloads and markers.

### Start Fuzzer

Starts a new fuzzer instance with specified configuration. The fuzzer will send HTTP requests with different payloads based on markers and wordlists.

```http
POST /api/fuzzer/start
```

**Request Body:**

```json
{
  "collection": "fuzzer_results",
  "request": "GET /api/test?param=§FUZZ§ HTTP/1.1\r\nHost: example.com\r\nUser-Agent: §AGENT§\r\n\r\n",
  "host": "example.com",
  "port": "443",
  "useTLS": true,
  "http2": false,
  "markers": {
    "§FUZZ§": "/path/to/payloads.txt",
    "§AGENT§": ["Mozilla/5.0", "curl/7.68"]
  },
  "mode": "cluster_bomb",
  "concurrency": 40,
  "timeout": 10.0,
  "generated_by": "manual"
}
```

> **Note:** Each marker value can be either a **string** (file path to a wordlist) or an **array of strings** (inline payloads). You can mix both types in the same request. Inline payloads support multi-line values since they are iterated by index, not split by newlines.

**Fields:**

- `collection` (string, required): Name of the database collection to store results
- `request` (string, required): Raw HTTP request template with markers
  - Markers are placeholders (e.g., `FUZZ`, `USERAGENT`) that will be replaced with wordlist values
- `host` (string, required): Target hostname (http:// or https:// prefix will be stripped)
- `port` (string, optional): Target port
  - Defaults to 443 for HTTPS (useTLS: true)
  - Defaults to 80 for HTTP (useTLS: false)
- `useTLS` (boolean, optional): Whether to use TLS/HTTPS (default: false)
- `http2` (boolean, optional): Whether to use HTTP/2 protocol (default: false)
- `markers` (object, required): Map of marker names to payload sources. Each value can be:
  - **string** — file path to a wordlist (one value per line)
  - **array of strings** — inline payloads (iterated by index, supports multi-line values)
  - You can mix both types in the same request
- `mode` (string, optional): Fuzzing mode (default: "cluster_bomb")
  - `"cluster_bomb"`: All combinations of all wordlists (Cartesian product)
  - `"pitch_fork"`: Synchronized iteration through wordlists (parallel)
- `concurrency` (integer, optional): Number of concurrent requests (default: 40)
- `timeout` (float, optional): Request timeout in seconds (default: 10.0)
- `process_data` (any, optional): Arbitrary data to associate with the fuzzer process
- `generated_by` (string, optional): Identifier for what generated this fuzzer request (e.g., "manual", "workflow")

**Response:**

```json
{
  "status": "started",
  "process_id": "______________1",
  "fuzzer_id": "______________1"
}
```

**Response Fields:**

- `status` (string): `"started"` on success
- `process_id` (string): Process ID for tracking in `_process` collection
- `fuzzer_id` (string): Unique fuzzer ID for stopping this fuzzer instance

**Features:**

- **Automatic Collection Creation**: Creates the specified collection if it doesn't exist
- **Real-time Results**: Results are saved to the database as they are received
- **Asynchronous Execution**: Fuzzer runs in background, endpoint returns immediately
- **Process Tracking**: Status tracked in `_process` collection
- **Marker Replacement**: Supports multiple markers with different wordlists
- **HTTP/2 Support**: Can fuzz HTTP/2 endpoints
- **Parsed Results**: Automatically parses requests and responses

**Fuzzing Modes:**

1. **Cluster Bomb** (`"cluster_bomb"`):

   - Tests all possible combinations of all wordlists
   - If you have 2 markers with 10 values each, generates 100 requests (10 × 10)
   - Best for comprehensive testing

2. **Pitch Fork** (`"pitch_fork"`):
   - Tests values from wordlists in parallel
   - If you have 2 markers with 10 values each, generates 10 requests
   - Values are matched by index (1st value from each list, 2nd from each, etc.)
   - Best for credential stuffing or paired values

**Error Responses:**

- 400 Bad Request - Invalid configuration or missing fields
  - Empty request
  - Empty host
  - Missing or empty markers
  - Empty string marker value (file path)
  - Empty array marker value (payloads)
  - Invalid marker type (not string or array)
- 403 Forbidden - Not authenticated
- 500 Internal Server Error - Failed to create collection or start fuzzer

**Example (Cluster Bomb):**

```json
{
  "collection": "admin_fuzz",
  "request": "POST /admin/login HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n{\"username\":\"USERNAME\",\"password\":\"PASSWORD\"}",
  "host": "example.com",
  "port": "443",
  "useTLS": true,
  "markers": {
    "USERNAME": "usernames.txt",
    "PASSWORD": "passwords.txt"
  },
  "mode": "cluster_bomb",
  "concurrency": 20,
  "timeout": 15.0
}
```

This will test all username/password combinations from the wordlists.

**Example (Pitch Fork):**

```json
{
  "collection": "api_enum",
  "request": "GET /api/v1/ENDPOINT HTTP/1.1\r\nHost: api.example.com\r\nAuthorization: Bearer TOKEN\r\n\r\n",
  "host": "api.example.com",
  "useTLS": true,
  "markers": {
    "ENDPOINT": "endpoints.txt",
    "TOKEN": "tokens.txt"
  },
  "mode": "pitch_fork",
  "concurrency": 30
}
```

This will test endpoints with corresponding tokens (1st endpoint with 1st token, etc.).

**Example (Inline Payloads):**

```json
{
  "collection": "xss_fuzz",
  "request": "GET /search?q=§FUZZ§ HTTP/1.1\r\nHost: example.com\r\n\r\n",
  "host": "example.com",
  "useTLS": true,
  "markers": {
    "§FUZZ§": [
      "<script>alert(1)</script>",
      "' OR 1=1 --",
      "../../../etc/passwd",
      "{{7*7}}"
    ]
  },
  "concurrency": 10,
  "timeout": 15.0
}
```

This will test the search parameter with inline payloads without needing a wordlist file.

**Example (Mixed — Wordlist + Inline Payloads):**

```json
{
  "collection": "cred_fuzz",
  "request": "POST /login HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n{\"user\":\"§USER§\",\"pass\":\"§PASS§\"}",
  "host": "example.com",
  "useTLS": true,
  "markers": {
    "§USER§": "usernames.txt",
    "§PASS§": ["password1", "123456", "qwerty"]
  },
  "mode": "pitch_fork",
  "concurrency": 10
}
```

This uses a wordlist file for usernames and inline payloads for passwords, paired 1:1 in pitch_fork mode.

---

### Stop Fuzzer

Stops a running fuzzer instance immediately.

```http
POST /api/fuzzer/stop
```

**Request Body:**

```json
{
  "id": "______________1"
}
```

**Fields:**

- `id` (string, required): The unique fuzzer ID to stop

**Response:**

```json
{
  "status": "stopped",
  "process_id": "______________1",
  "fuzzer_id": "______________1"
}
```

**Features:**

- Immediately terminates fuzzing
- Updates process state to "Killed" in database
- Records final progress at time of stop
- Cleans up fuzzer instance from memory

**Error Responses:**

- 400 Bad Request - Missing or empty ID
- 403 Forbidden - Not authenticated
- 404 Not Found - Fuzzer with specified ID not found

---

## Commands

### Run Command

Executes a shell command and tracks its execution.

```http
POST /api/runcommand
```

**Request Body:**

```json
{
  "command": "nuclei -u https://example.com -t vulnerabilities/",
  "data": {...},
  "saveTo": "collection|file",
  "collection": "nuclei_results",
  "filename": "scan-output.txt"
}
```

**Fields:**

- `command` (string, required): Shell command to execute
- `data` (any, optional): Additional metadata to store
- `saveTo` (string, required): Where to save output ("collection" or "file")
- `collection` (string, conditional): Collection name (required if saveTo is "collection")
- `filename` (string, conditional): File path (required if saveTo is "file")

**Response:**

```json
{
  "id": "process_id"
}
```

**Features:**

- Asynchronous execution
- Process state tracked in `_process` collection
- Line-by-line output capture (collection mode)
- Cross-platform (bash on Unix, cmd on Windows)

---

## Data Storage

### Fuzzer Results Collection

Each fuzzer creates/uses a collection to store results. Each result record contains:

**Schema:**

```json
{
  "fuzzer_id": "string",           // ID of the fuzzer that generated this result
  "time": 123456789,                // Response time in nanoseconds
  "markers": {                      // Marker values used in this request
    "FUZZ": "payload-value",
    "PARAM": "param-value"
  },
  "raw_request": "string",          // Complete raw HTTP request sent
  "raw_response": "string",         // Complete raw HTTP response received
  "req_method": "GET",              // HTTP method
  "req_url": "/api/test",           // Request URL/path
  "req_version": "HTTP/1.1",        // HTTP version
  "req_headers": {...},             // Request headers (JSON object)
  "resp_version": "HTTP/1.1",       // Response HTTP version
  "resp_status": 200,               // Response status code (integer)
  "resp_status_full": "200 OK",     // Full response status line
  "resp_length": 1234,              // Response size in bytes
  "resp_headers": {...}             // Response headers (JSON object)
}
```

**Example Result:**

```json
{
  "id": "result_record_id",
  "fuzzer_id": "______________1",
  "time": 123456789,
  "markers": {
    "FUZZ": "admin",
    "PASSWORD": "password123"
  },
  "raw_request": "POST /login HTTP/1.1\r\nHost: example.com\r\n...",
  "raw_response": "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n...",
  "req_method": "POST",
  "req_url": "/login",
  "req_version": "HTTP/1.1",
  "req_headers": {
    "Host": "example.com",
    "Content-Type": "application/json"
  },
  "resp_version": "HTTP/1.1",
  "resp_status": 200,
  "resp_status_full": "200 OK",
  "resp_length": 45,
  "resp_headers": {
    "Content-Type": "application/json",
    "Set-Cookie": "session=abc123"
  }
}
```

### Process Tracking

Fuzzer execution is tracked in the `_process` collection with states:

- `"In Queue"` - Fuzzer registered but not started
- `"Running"` - Fuzzer actively sending requests
- `"Completed"` - Fuzzer finished all requests
- `"Killed"` - Fuzzer manually stopped
- `"Failed"` - Fuzzer encountered an error

---

## Analyzing Results

### Querying Results

Use PocketBase query API to filter results:

**Find successful responses:**

```
collection: fuzzer_results
filter: resp_status >= 200 && resp_status < 300
```

**Find specific marker values:**

```
filter: markers.FUZZ = 'admin'
```

**Find large responses:**

```
filter: resp_length > 10000
sort: -resp_length
```

**Find by status code:**

```
filter: resp_status = 403
```

### Best Practices

1. **Collection Naming**: Use descriptive names like `"api_fuzzing_2024"` instead of generic names
2. **Wordlist Size**: Be mindful of wordlist sizes in cluster_bomb mode (multiplication effect)
3. **Concurrency**: Adjust based on target capacity and network conditions
4. **Timeout**: Set appropriate timeouts for slow endpoints
5. **Monitoring**: Check `_process` collection for fuzzer status
6. **Cleanup**: Stop fuzzers when done to free resources

---

## Error Responses

**Standard Error Format:**

```json
{
  "error": "Error description"
}
```

**Common Status Codes:**

- **400 Bad Request** - Invalid request body or parameters
  - Missing required fields
  - Empty values where not allowed
  - Invalid file paths
- **403 Forbidden** - Not authenticated
- **404 Not Found** - Fuzzer ID not found
- **500 Internal Server Error** - Server-side error
  - Failed to create collection
  - Failed to start fuzzer
  - Failed to read wordlist file

---

## Marker Types

Each marker in the `markers` object can be one of two types:

### String — Wordlist File Path

Provide a file path and the fuzzer reads it line by line:

```json
{
  "markers": {
    "§FUZZ§": "/path/to/wordlist.txt"
  }
}
```

### Array — Inline Payloads

Provide values directly as an array. Each element is used as-is (supports multi-line values):

```json
{
  "markers": {
    "§FUZZ§": ["admin", "test", "root", "multi\nline\nvalue"]
  }
}
```

### Wordlist File Format

Wordlist files should be plain text with one value per line:

**Example (`payloads.txt`):**

```
admin
administrator
root
user
test
```

**Example (`passwords.txt`):**

```
password
123456
admin123
letmein
```

**Tips:**

- UTF-8 encoding recommended
- One payload per line
- Empty lines are ignored
- No special formatting required
- Can use absolute or relative paths

---

## Performance

### Concurrency Guidelines

- **Light testing**: 10-20 concurrent requests
- **Normal testing**: 20-50 concurrent requests
- **Aggressive testing**: 50-100 concurrent requests
- **Rate-limited targets**: 1-10 concurrent requests

### Memory Usage

- Each fuzzer instance maintains a results channel
- Results are immediately saved to database
- Completed results are not held in memory
- Stop fuzzer when done to free resources

---

## Security Considerations

1. **Authorization**: Ensure you have permission to test the target
2. **Rate Limiting**: Respect target server capacity
3. **Legal**: Only test systems you own or have explicit permission to test
4. **Network**: Be aware of network traffic generated
5. **Data**: Fuzzer results may contain sensitive data (passwords, tokens, etc.)

---

## Examples

### Basic Path Fuzzing

```json
{
  "collection": "path_fuzz",
  "request": "GET /FUZZ HTTP/1.1\r\nHost: example.com\r\n\r\n",
  "host": "example.com",
  "useTLS": true,
  "markers": {
    "FUZZ": "common-paths.txt"
  },
  "concurrency": 30
}
```

### Header Injection Testing

```json
{
  "collection": "header_injection",
  "request": "GET /api/data HTTP/1.1\r\nHost: example.com\r\nX-Custom-Header: PAYLOAD\r\n\r\n",
  "host": "api.example.com",
  "useTLS": true,
  "markers": {
    "PAYLOAD": "injection-payloads.txt"
  },
  "concurrency": 20
}
```

### POST Data Fuzzing

```json
{
  "collection": "post_data_fuzz",
  "request": "POST /search HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/x-www-form-urlencoded\r\n\r\nq=SEARCH_TERM",
  "host": "example.com",
  "useTLS": true,
  "markers": {
    "SEARCH_TERM": "search-payloads.txt"
  },
  "concurrency": 25
}
```

### API Version Enumeration

```json
{
  "collection": "api_version_enum",
  "request": "GET /api/VERSION/users HTTP/1.1\r\nHost: api.example.com\r\n\r\n",
  "host": "api.example.com",
  "useTLS": true,
  "markers": {
    "VERSION": "versions.txt"
  },
  "mode": "cluster_bomb",
  "concurrency": 15
}
```

---

## Authentication

All API endpoints require authentication via:

- Admin credentials
- Authenticated user record

Unauthenticated requests return `403 Forbidden`.

---

---

## Support

For issues or questions:

- Check process state in `_process` collection
- Review collection schemas
- Verify wordlist file paths
- Check server logs for detailed errors
