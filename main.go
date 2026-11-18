// The entry <-> env lifecycle (main.ts): the Go binary binds an ephemeral port,
// POSTs {"port": N} to $MAKEFUN_ENV_URL/register, polls GET /done until
// {"done": true}, and exits 0. main.ts spawns this with stdio inherit and
// exits with its code.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

// register POSTs the bound port until the env accepts it (200 or 409); a 400
// or 405 ends the program.
func register(envURL string, port int) error {
	payload := []byte(`{"port":` + strconv.Itoa(port) + `}`)
	client := &http.Client{Timeout: requestTimeoutMs}
	for attempt := 0; ; attempt++ {
		resp, err := client.Post(envURL+"/register", "application/json", bytes.NewReader(payload))
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 || resp.StatusCode == 409 {
				return nil
			}
			if resp.StatusCode == 400 || resp.StatusCode == 405 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				return fmt.Errorf("register answered HTTP %d: %s", resp.StatusCode, body)
			}
		}
		time.Sleep(retryDelay(attempt))
	}
}

// waitForDone polls GET /done until it answers {"done":true}.
func waitForDone(envURL string) {
	client := &http.Client{Timeout: requestTimeoutMs}
	for attempt := 0; ; attempt++ {
		func() {
			resp, err := client.Get(envURL + "/done")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var body struct {
					Done bool `json:"done"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Done {
					os.Exit(0)
				}
			}
		}()
		time.Sleep(retryDelay(attempt))
	}
}

func main() {
	debugSetGCPercent()

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

	waitForDone(envURL) // exits 0 when done

	ln.Close()
	os.Stdout.WriteString("[entry] done\n")
}

func envURLTrimmed(raw string) string {
	for len(raw) > 0 && raw[len(raw)-1] == '/' {
		raw = raw[:len(raw)-1]
	}
	return raw
}
