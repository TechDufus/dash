package upstream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// A pooled keep-alive connection that the server closes while a request is on it fails that
// request with a bare EOF, unless net/http may send the request again (Request.isReplayable). These
// tests drive that with a server that answers the first request of every connection and, on the
// second, reads the request to its last byte and then closes the connection without a word.
// Nothing sleeps: the server acts on what it reads, and the client's retry happens before its call
// returns, so the events are complete when the call is.

// silenceID is a well-formed silence id, for ExpireSilence.
const silenceID = "00000000-1111-2222-3333-444444444444"

// seen is one request the server read.
type seen struct {
	conn, seq    int // the connection (from 1) and the request's place on it (from 1)
	method, path string
	body         string
	idemKey      bool // whether an Idempotency-Key line reached the wire
}

func (s seen) String() string {
	return fmt.Sprintf("conn %d #%d %s %s %q", s.conn, s.seq, s.method, s.path, s.body)
}

// closingServer answers the first request of each connection and drops the connection on its
// second request, after reading it whole.
type closingServer struct {
	URL string

	mu     sync.Mutex
	conns  int
	events []seen
}

func newClosingServer(t *testing.T) *closingServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &closingServer{URL: "http://" + ln.Addr().String()}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		open []net.Conn
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			open = append(open, c)
			mu.Unlock()
			s.mu.Lock()
			s.conns++
			id := s.conns
			s.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serveConn(c, id)
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range open {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return s
}

func (s *closingServer) serveConn(c net.Conn, id int) {
	defer c.Close()
	br := bufio.NewReader(c)
	for seq := 1; ; seq++ {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		body, _ := io.ReadAll(req.Body)
		_, idem := req.Header["Idempotency-Key"]
		s.mu.Lock()
		s.events = append(s.events, seen{conn: id, seq: seq, method: req.Method, path: req.URL.Path, body: string(body), idemKey: idem})
		s.mu.Unlock()
		if seq > 1 {
			return // the request was read whole; the connection ends here with no answer
		}
		out := answerFor(req)
		if _, err := fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(out), out); err != nil {
			return
		}
	}
}

func answerFor(r *http.Request) string {
	switch {
	case r.URL.Path == "/api/v1/query":
		return `{"status":"success","data":{"resultType":"vector","result":[]}}`
	case r.URL.Path == "/api/v1/query_range":
		return `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	case r.Method == http.MethodPost:
		return `{"silenceID":"` + silenceID + `"}`
	default:
		return `[]`
	}
}

func (s *closingServer) log() []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seen(nil), s.events...)
}

// spyTransport is a plugin transport in miniature: like the plugin host's, it sends a clone of
// the request, and it keeps what the request looked like when it left the client.
type spyTransport struct {
	rt http.RoundTripper

	mu         sync.Mutex
	marked     bool // an Idempotency-Key entry, which Transport.isReplayable accepts with no value
	hasGetBody bool
}

func (s *spyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	s.mu.Lock()
	_, s.marked = r.Header["Idempotency-Key"]
	s.hasGetBody = r.GetBody != nil
	s.mu.Unlock()
	return s.rt.RoundTrip(r)
}

// newSpy is a spyTransport over a transport of its own, so that no test shares a pool with
// another or with http.DefaultTransport.
func newSpy(t *testing.T) *spyTransport {
	t.Helper()
	tr := &http.Transport{MaxIdleConnsPerHost: 4}
	t.Cleanup(tr.CloseIdleConnections)
	return &spyTransport{rt: tr}
}

// firstAttempt records whether the first connection a call got was one taken from the pool.
func firstAttempt(reused *bool) context.Context {
	var once sync.Once
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		once.Do(func() { *reused = i.Reused })
	}})
}

// Every query dash sends to VictoriaMetrics is a read, so one that meets a connection the server
// has just closed is sent again on a new one, with the same body, instead of failing the poll.
func TestPromQueryIsSentAgainWhenThePooledConnectionWasClosed(t *testing.T) {
	queries := map[string]func(*Prom, context.Context) error{
		"instant": func(p *Prom, c context.Context) error {
			_, err := p.Instant(c, `up{job="node"}`)
			return err
		},
		"range": func(p *Prom, c context.Context) error {
			now := time.Unix(1_800_000_000, 0)
			_, err := p.Range(c, `rate(x[5m])`, now.Add(-time.Hour), now, time.Minute)
			return err
		},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			srv := newClosingServer(t)
			spy := newSpy(t)
			p := NewProm(srv.URL, spy)

			if err := query(p, ctx); err != nil {
				t.Fatalf("first query: %v", err)
			}
			var reused bool
			if err := query(p, firstAttempt(&reused)); err != nil {
				t.Errorf("query on a connection the server closed: %v, want it sent again and answered\nserver saw:\n%v", err, srv.log())
			}
			if !reused {
				t.Fatal("the second query did not start on the pooled connection: the test does not reach the failure it guards")
			}

			ev := srv.log()
			if len(ev) != 3 {
				t.Fatalf("server saw %d requests, want 3 (answered, dropped, sent again):\n%v", len(ev), ev)
			}
			if ev[1].conn != ev[0].conn || ev[1].seq != 2 {
				t.Errorf("the dropped request is %v, want the second on the first request's connection", ev[1])
			}
			if ev[2].conn == ev[0].conn || ev[2].seq != 1 {
				t.Errorf("the new attempt is %v, want the first request of a new connection", ev[2])
			}
			if ev[2].method != http.MethodPost || ev[2].path != ev[1].path || ev[2].body != ev[1].body || ev[2].body == "" {
				t.Errorf("the new attempt differs from the dropped one:\n%v\n%v", ev[1], ev[2])
			}
			for _, e := range ev {
				if e.idemKey {
					t.Errorf("%v: an Idempotency-Key line reached the wire; the marker is for the client only", e)
				}
			}
			if !spy.marked || !spy.hasGetBody {
				t.Errorf("the request left the client with idempotency marker=%v, GetBody=%v; both are needed for a replay", spy.marked, spy.hasGetBody)
			}
		})
	}
}

// A request that changes something upstream is never sent again: it fails with the EOF, and the
// server has seen it once, even though a retry would have been answered.
func TestMutatingRequestsAreNeverSentAgain(t *testing.T) {
	steps := map[string]func(*Alertmanager, context.Context) error{
		"silence create": func(a *Alertmanager, c context.Context) error {
			_, err := a.CreateSilence(c, SilenceSpec{
				Matchers: []model.SilenceMatcher{{Name: "alertname", Value: "Foo"}},
				StartsAt: time.Unix(1_800_000_000, 0), EndsAt: time.Unix(1_800_003_600, 0),
				CreatedBy: "alice", Comment: "maintenance",
			})
			return err
		},
		"silence expire": func(a *Alertmanager, c context.Context) error { return a.ExpireSilence(c, silenceID) },
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			srv := newClosingServer(t)
			spy := newSpy(t)
			am := NewAlertmanager(srv.URL, spy, nil)

			if _, err := am.Silences(ctx); err != nil { // the request that pools the connection
				t.Fatalf("list: %v", err)
			}
			var reused bool
			err := step(am, firstAttempt(&reused))
			if !reused {
				t.Fatal("the mutation did not start on the pooled connection: the test does not reach the failure it guards")
			}
			if !errors.Is(err, io.EOF) {
				t.Errorf("err = %v, want the EOF of the dropped connection", err)
			}
			if spy.marked {
				t.Error("a mutating request carries an Idempotency-Key entry: Transport would send it again")
			}

			ev := srv.log()
			if len(ev) != 2 {
				t.Fatalf("server saw %d requests, want 2 (the list, the mutation once):\n%v", len(ev), ev)
			}
			if ev[1].conn != ev[0].conn || ev[1].seq != 2 {
				t.Errorf("the mutation is %v, want the second request on the list's connection", ev[1])
			}
		})
	}
}
