// The entry <-> env lifecycle (main.ts): the Go binary binds an ephemeral port,
// POSTs {"port": N} to $MAKEFUN_ENV_URL/register, polls GET /done until
// {"done": true}, and exits 0. main.ts spawns this with stdio inherit and
// exits with its code. The lifecycle's two requests ride a hand-framed
// HTTP/1.1 client over net.Dial (net/http's client semantics are not needed
// for three fixed requests).
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

const requestTimeoutMs = 30_000
const retryInitialMs = 250 * time.Millisecond
const retryMaxMs = 2_000 * time.Millisecond

func retryDelay(attempt int) time.Duration {
	d := retryInitialMs
	for i := 0; i < attempt && i < 8; i++ {
		d *= 2
		if d > retryMaxMs {
			return retryMaxMs
		}
	}
	if d > retryMaxMs {
		return retryMaxMs
	}
	return d
}

// httpExchange issues one hand-framed HTTP/1.1 request over a fresh
// connection and answers (status, body). The env answers small JSON bodies
// with an explicit Content-Length; a chunked or absent length is read to EOF.
func httpExchange(envURL, method, target, contentType, body string) (int, []byte, error) {
	conn, err := net.DialTimeout("tcp", hostPortOf(envURL), 5*time.Second)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(requestTimeoutMs * time.Millisecond))

	var head []byte
	head = append(head, method...)
	head = append(head, ' ')
	head = append(head, target...)
	head = append(head, " HTTP/1.1\r\nhost: "...)
	head = append(head, hostPortOf(envURL)...)
	head = append(head, "\r\nconnection: close\r\n"...)
	if contentType != "" {
		head = append(head, "content-type: "...)
		head = append(head, contentType...)
		head = append(head, "\r\n"...)
	}
	if body != "" {
		head = append(head, "content-length: "...)
		head = append(head, strconv.Itoa(len(body))...)
		head = append(head, "\r\n"...)
	}
	head = append(head, "\r\n"...)

	writes := 0
	for len(head) > 0 || len(body) > 0 {
		var chunk []byte
		if len(head) > 0 {
			chunk = head
			head = nil
		} else {
			chunk = []byte(body)
			body = ""
		}
		// One request write; the whole head+body goes in as few writes as the
		// connection accepts, and a short write continues.
		n, werr := conn.Write(chunk)
		writes++
		if werr != nil {
			return 0, nil, werr
		}
		if n < len(chunk) {
			// partial write: keep going
			rest := chunk[n:]
			head = rest // re-enter the loop with the remainder as the head
			if len(rest) == 0 {
				break
			}
		}
	}
	_ = writes

	// Read the whole response (connection: close bounds it).
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, rerr := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	return parseResponse(buf)
}

// parseResponse reads a status line, the headers (to find the length) and the body.
func parseResponse(buf []byte) (int, []byte, error) {
	// status line
	lineEnd := indexCRLF(buf)
	if lineEnd < 0 {
		return 0, nil, fmt.Errorf("no status line in %d bytes", len(buf))
	}
	// "HTTP/1.1 200 OK"
	status := 0
	if len(buf) >= 13 && string(buf[:5]) == "HTTP/" {
		if v, err := strconv.Atoi(string(buf[9:12])); err == nil {
			status = v
		}
	}
	pos := lineEnd + 2
	contentLength := -1
	chunked := false
	for pos < len(buf) {
		lineEnd := indexCRLF(buf[pos:])
		if lineEnd < 0 {
			break
		}
		line := string(buf[pos : pos+lineEnd])
		pos += lineEnd + 2
		if line == "" {
			break
		}
		if i := indexByte(line, ':'); i >= 0 {
			name := lower(line[:i])
			value := trimSpace(line[i+1:])
			if name == "content-length" {
				if v, err := strconv.Atoi(value); err == nil {
					contentLength = v
				}
			} else if name == "transfer-encoding" && contains(value, "chunked") {
				chunked = true
			}
		}
	}
	rest := buf[pos:]
	if chunked {
		rest = dechunkResponse(rest)
	} else if contentLength >= 0 && len(rest) > contentLength {
		rest = rest[:contentLength]
	}
	return status, rest, nil
}

func indexCRLF(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == '\r' && b[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func contains(s, sub string) bool {
	return indexByteSeq(s, sub) >= 0
}

func indexByteSeq(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}

func lower(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 32
		}
	}
	return string(out)
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// dechunkResponse removes a chunked body's framing.
func dechunkResponse(b []byte) []byte {
	var out []byte
	pos := 0
	for pos < len(b) {
		lineEnd := indexCRLF(b[pos:])
		if lineEnd < 0 {
			break
		}
		sizeText := string(b[pos : pos+lineEnd])
		if i := indexByte(sizeText, ';'); i >= 0 {
			sizeText = sizeText[:i]
		}
		size, err := strconv.ParseInt(sizeText, 16, 64)
		if err != nil || size < 0 {
			break
		}
		pos += lineEnd + 2
		if size == 0 {
			break
		}
		if pos+int(size) > len(b) {
			out = append(out, b[pos:]...)
			break
		}
		out = append(out, b[pos:pos+int(size)]...)
		pos += int(size) + 2
	}
	return out
}

// hostPortOf answers "host:port" from an http:// URL.
func hostPortOf(envURL string) string {
	rest := envURL
	if i := indexByteSeq(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := indexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// register POSTs the bound port until the env accepts it (200 or 409); a 400
// or 405 ends the program.
func register(envURL string, port int) error {
	for attempt := 0; ; attempt++ {
		status, body, err := httpExchange(envURL, "POST", "/register", "application/json", `{"port":`+strconv.Itoa(port)+`}`)
		if err == nil {
			if status == 200 || status == 409 {
				return nil
			}
			if status == 400 || status == 405 {
				return fmt.Errorf("register answered HTTP %d: %s", status, body)
			}
		}
		time.Sleep(retryDelay(attempt))
	}
}

// waitForDone polls GET /done until it answers {"done":true}, then returns.
func waitForDone(envURL string) {
	for attempt := 0; ; attempt++ {
		status, body, err := httpExchange(envURL, "GET", "/done", "", "")
		if err == nil && status >= 200 && status < 300 {
			var parsed struct {
				Done bool `json:"done"`
			}
			if json.Unmarshal(body, &parsed) == nil && parsed.Done {
				return
			}
		}
		time.Sleep(retryDelay(attempt))
	}
}

func main() {
	debugSetGCPercent()
	startPprofIfEnabled()
	defer stopCPUProfile()

	envURL := envURLTrimmed(os.Getenv("MAKEFUN_ENV_URL"))
	if envURL == "" {
		os.Stderr.WriteString("[entry] MAKEFUN_ENV_URL is not set: there is no env to register with\n")
		os.Exit(1)
	}

	f, token := buildApp()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Stderr.WriteString("[entry] listen: " + err.Error() + "\n")
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	go serve(f, token, ln)
	os.Stdout.WriteString("[entry] listening on port " + strconv.Itoa(port) + "\n")

	if err := register(envURL, port); err != nil {
		os.Stderr.WriteString("[entry] " + err.Error() + "\n")
		ln.Close()
		os.Exit(1)
	}
	os.Stdout.WriteString("[entry] registered; waiting for the env to say done\n")

	waitForDone(envURL)

	ln.Close()
	os.Stdout.WriteString("[entry] done\n")
}

func envURLTrimmed(raw string) string {
	for len(raw) > 0 && raw[len(raw)-1] == '/' {
		raw = raw[:len(raw)-1]
	}
	return raw
}
