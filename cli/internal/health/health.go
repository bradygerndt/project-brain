// Package health checks a running instance's /health endpoint, same
// contract as bin/brain.js's fetchHealth (2s timeout, nil on any failure).
package health

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Status struct {
	OK       bool   `json:"ok"`
	Brain    string `json:"brain"`
	Sessions int    `json:"sessions"`
}

var client = &http.Client{Timeout: 2 * time.Second}

// Fetch hits http://{host}:{port}/health and returns nil on any failure
// (connection refused, timeout, non-2xx, bad JSON) — mirrors the "offline"
// semantics used by `brain ps`. host is "127.0.0.1" for Docker-managed
// instances, or a remote instance's registered Host.
func Fetch(host string, port int) *Status {
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/health", host, port))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var s Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil
	}
	return &s
}
