// Package bridge implements the stdio<->HTTP proxy that lets a local stdio
// MCP client (Claude Desktop) talk to a project-brain instance's Streamable
// HTTP transport (src/server.ts's POST/GET/DELETE /mcp handlers), without
// depending on the third-party mcp-remote/npx bridge.
//
// v1 scope is synchronous request/response only: each newline-delimited
// message read from stdin is POSTed to /mcp and its response is written
// back to stdout in the same framing. Consuming the GET /mcp SSE stream for
// server-initiated notifications is not implemented — nothing in
// project-brain sends those yet, so there's no unused plumbing for it here.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SessionHeader is the header the server uses to correlate requests with a
// session: it's returned on the first response after initialize
// (src/server.ts sets `mcp-session-id`) and must be echoed on every
// subsequent request for the same session.
const SessionHeader = "Mcp-Session-Id"

// Bridge proxies newline-delimited JSON-RPC messages between stdio and a
// single project-brain instance's /mcp endpoint. The zero value is not
// usable; construct with New.
type Bridge struct {
	url    string
	client *http.Client

	mu        sync.Mutex
	sessionID string
}

// New creates a Bridge that talks to the given /mcp URL, e.g.
// "http://127.0.0.1:3579/mcp" or "http://100.x.y.z:3579/mcp" for a remote
// instance.
func New(url string) *Bridge {
	return &Bridge{
		url:    url,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// SessionID returns the session id captured from the server's first
// response, or "" if no session has been established yet.
func (b *Bridge) SessionID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessionID
}

func (b *Bridge) setSessionID(id string) {
	b.mu.Lock()
	b.sessionID = id
	b.mu.Unlock()
}

// Send POSTs one JSON-RPC message (exactly one line of stdin framing) to
// /mcp and returns the JSON-RPC message(s) found in the response, each
// ready to be written out as one NDJSON line.
//
// The server's Streamable HTTP transport (the MCP SDK's
// StreamableHTTPServerTransport, used without enableJsonResponse) always
// answers a request-bearing POST over text/event-stream, even though the
// request's Accept header also lists application/json — Send transparently
// unwraps that SSE framing so callers only ever see raw JSON-RPC. A
// notifications/responses-only message gets a bare 202 with no body, which
// Send reports as zero messages, not an error.
func (b *Bridge) Send(ctx context.Context, msg []byte) ([][]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sid := b.SessionID(); sid != "" {
		req.Header.Set(SessionHeader, sid)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get(SessionHeader); sid != "" {
		b.setSessionID(sid)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("%s: %s: %s", b.url, resp.Status, strings.TrimSpace(string(body)))
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return parseSSE(body), nil
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, nil
	}
	return [][]byte{trimmed}, nil
}

// Close sends DELETE /mcp to close the session cleanly, matching
// src/server.ts's app.delete('/mcp', ...) handler. The captured session id
// (if any) is sent as the Mcp-Session-Id header; DELETE is still sent with
// no such header when no session was ever established (the server no-ops
// on that, same as it does for an unknown session id).
func (b *Bridge) Close(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.url, nil)
	if err != nil {
		return err
	}
	if sid := b.SessionID(); sid != "" {
		req.Header.Set(SessionHeader, sid)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Run reads newline-delimited JSON-RPC messages from in and proxies each to
// /mcp via Send, writing the response message(s) back to out in the same
// framing. It returns when in reaches EOF or ctx is cancelled (the caller
// wires SIGINT/SIGTERM into ctx); either way, Run sends a final Close
// before returning. A per-message Send failure is reported on errOut (pass
// nil to discard) rather than aborting the bridge — one bad message
// shouldn't take down the whole session.
func (b *Bridge) Run(ctx context.Context, in io.Reader, out io.Writer, errOut io.Writer) error {
	if errOut == nil {
		errOut = io.Discard
	}

	lines := make(chan []byte)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			cp := make([]byte, len(line))
			copy(cp, line)
			select {
			case lines <- cp:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(errOut, "mcp-bridge: reading stdin: %v\n", err)
		}
	}()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case line, ok := <-lines:
			if !ok {
				break loop
			}
			responses, err := b.Send(ctx, line)
			if err != nil {
				fmt.Fprintf(errOut, "mcp-bridge: %v\n", err)
				continue
			}
			for _, r := range responses {
				out.Write(r)
				out.Write([]byte("\n"))
			}
		}
	}

	// Use a fresh context for the closing DELETE: ctx may already be
	// cancelled (SIGINT/SIGTERM), and a cancelled context would make the
	// request fail before it's even sent.
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Close(closeCtx); err != nil {
		fmt.Fprintf(errOut, "mcp-bridge: closing session: %v\n", err)
	}
	return nil
}

// parseSSE extracts the JSON-RPC message(s) carried in an SSE-framed
// response body — one message per event, per the "event: message\ndata:
// <json>\n\n" framing StreamableHTTPServerTransport emits. Multi-line
// "data:" fields within one event are joined with "\n", per the SSE spec;
// every other field (event:, id:, retry:, comments) is ignored, since the
// bridge only forwards JSON-RPC payloads.
func parseSSE(body []byte) [][]byte {
	var messages [][]byte
	var dataLines []string
	flush := func() {
		if len(dataLines) > 0 {
			messages = append(messages, []byte(strings.Join(dataLines, "\n")))
			dataLines = nil
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(data, " "))
		}
	}
	flush()
	return messages
}
