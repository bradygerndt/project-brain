package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer stands in for src/server.ts's /mcp handlers closely enough to
// exercise the bridge: POST answers over SSE framing (the real server never
// sets enableJsonResponse, so it always does this — see Send's doc
// comment), assigns a session id on the first request, and requires it on
// every later one; DELETE just records that it was called.
type fakeServer struct {
	mu       sync.Mutex
	sawPost  []*http.Request
	postBody [][]byte
	deletes  []*http.Request
}

func newFakeServer(t *testing.T) (*httptest.Server, *fakeServer) {
	t.Helper()
	fs := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		fs.mu.Lock()
		fs.sawPost = append(fs.sawPost, r)
		fs.postBody = append(fs.postBody, body)
		n := len(fs.sawPost)
		fs.mu.Unlock()

		switch r.Method {
		case http.MethodPost:
			var msg map[string]any
			_ = json.Unmarshal(body, &msg)
			id := msg["id"]

			if n == 1 {
				w.Header().Set(SessionHeader, "session-abc-123")
			} else if r.Header.Get(SessionHeader) != "session-abc-123" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"missing or wrong session id"}`))
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			idJSON, _ := json.Marshal(id)
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"ok\":true}}\n\n", idJSON)
		case http.MethodDelete:
			fs.mu.Lock()
			fs.deletes = append(fs.deletes, r)
			fs.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fs
}

func readAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func TestSend_RequestShapeAndSSEUnwrapping(t *testing.T) {
	srv, fs := newFakeServer(t)
	b := New(srv.URL + "/mcp")

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	responses, err := b.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(fs.sawPost) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(fs.sawPost))
	}
	got := fs.sawPost[0]
	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	accept := got.Header.Get("Accept")
	if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
		t.Errorf("Accept = %q, want both application/json and text/event-stream", accept)
	}
	if !bytes.Equal(fs.postBody[0], req) {
		t.Errorf("POST body = %s, want %s", fs.postBody[0], req)
	}

	// The response came back SSE-framed; Send must hand back the bare
	// JSON-RPC payload, not the "event:/data:" wrapper.
	if len(responses) != 1 {
		t.Fatalf("expected 1 response message, got %d: %v", len(responses), responses)
	}
	var decoded map[string]any
	if err := json.Unmarshal(responses[0], &decoded); err != nil {
		t.Fatalf("response not valid JSON: %v (%s)", err, responses[0])
	}
	if decoded["id"] != float64(1) {
		t.Errorf("response id = %v, want 1", decoded["id"])
	}

	if b.SessionID() != "session-abc-123" {
		t.Errorf("SessionID() = %q, want session-abc-123", b.SessionID())
	}
}

func TestSend_SessionIDCarriesToSubsequentRequests(t *testing.T) {
	srv, fs := newFakeServer(t)
	b := New(srv.URL + "/mcp")

	if _, err := b.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := b.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)); err != nil {
		t.Fatalf("second Send: %v", err)
	}

	if len(fs.sawPost) != 2 {
		t.Fatalf("expected 2 POSTs, got %d", len(fs.sawPost))
	}
	if got := fs.sawPost[0].Header.Get(SessionHeader); got != "" {
		t.Errorf("first request should carry no session id yet, got %q", got)
	}
	if got := fs.sawPost[1].Header.Get(SessionHeader); got != "session-abc-123" {
		t.Errorf("second request session id = %q, want session-abc-123", got)
	}
}

func TestRun_DeleteFiresOnStdinEOF(t *testing.T) {
	srv, fs := newFakeServer(t)
	b := New(srv.URL + "/mcp")

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var out bytes.Buffer
	var errOut bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background(), in, &out, &errOut) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after stdin EOF")
	}

	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr output: %s", errOut.String())
	}
	if len(fs.deletes) != 1 {
		t.Fatalf("expected 1 DELETE after stdin EOF, got %d", len(fs.deletes))
	}
	if got := fs.deletes[0].Header.Get(SessionHeader); got != "session-abc-123" {
		t.Errorf("DELETE session id = %q, want session-abc-123", got)
	}

	// The one request's response should have been written to stdout,
	// newline-delimited.
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatal("expected a response line on stdout, got none")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("stdout line not valid JSON: %v (%s)", err, line)
	}
}

func TestRun_DeleteFiresEvenWithNoMessages(t *testing.T) {
	srv, fs := newFakeServer(t)
	b := New(srv.URL + "/mcp")

	in := strings.NewReader("")
	var out, errOut bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- b.Run(context.Background(), in, &out, &errOut) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on immediate EOF")
	}

	if out.Len() != 0 {
		t.Errorf("expected no stdout output, got %q", out.String())
	}
	if len(fs.deletes) != 1 {
		t.Fatalf("expected 1 DELETE even with no prior messages, got %d", len(fs.deletes))
	}
	if got := fs.deletes[0].Header.Get(SessionHeader); got != "" {
		t.Errorf("DELETE with no established session should carry no session header, got %q", got)
	}
}
