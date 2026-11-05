package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mcpListenBody opens a subscriptions/listen stream (protocol 2026-07-28) for the tool list.
const mcpListenBody = `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"_meta":{` +
	`"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},` +
	`"notifications":{"toolsListChanged":true}}}`

// listen opens a listen stream at base with token and returns its lines once the server has
// acknowledged it; the channel closes when the server ends the stream.
func listen(t *testing.T, base, token string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mcp", strings.NewReader(mcpListenBody))
	r.Host = testHost
	for k, v := range map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Authorization": "Bearer " + token,
		"Mcp-Protocol-Version": "2026-07-28", "Mcp-Method": "subscriptions/listen",
	} {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("listen = %d", resp.StatusCode)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	for l := range lines {
		if strings.Contains(l, "notifications/subscriptions/acknowledged") {
			return lines
		}
	}
	t.Fatal("the stream ended before its acknowledgement")
	return nil
}

// stillOpen fails when the server ends the stream within d.
func stillOpen(t *testing.T, lines <-chan string, d time.Duration, what string) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				t.Fatalf("%s ended", what)
			}
		case <-deadline:
			return
		}
	}
}

// An open listen stream ends at the first re-check after its token is revoked, while another
// token's stream carries on; switching MCP off ends every stream.
func TestMCPListenStreamsEndOnRevocationAndTheOffSwitch(t *testing.T) {
	e := newMCPEnv(t)
	e.s.heartbeat = 20 * time.Millisecond
	ts := httptest.NewServer(e.s.Handler())
	t.Cleanup(ts.Close)
	revoked, id := e.f.pat(t, "bob", "mcp", []string{"read"})

	gone := listen(t, ts.URL, revoked)
	kept := listen(t, ts.URL, e.readTok)
	stillOpen(t, gone, 100*time.Millisecond, "a live token's stream")
	if _, ok, err := e.f.store.RevokePAT(id); !ok || err != nil {
		t.Fatal(ok, err)
	}
	ended(t, gone, "the stream after the revocation")
	stillOpen(t, kept, 100*time.Millisecond, "another token's stream")

	e.on = false
	ended(t, kept, "the stream after MCP was switched off")
}
