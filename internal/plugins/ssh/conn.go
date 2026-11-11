package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	sshmodel "github.com/TechDufus/dash/internal/plugins/ssh/model"
)

// clientVersion is how dash names itself in the SSH handshake.
const clientVersion = "SSH-2.0-dash"

// scanAlgorithms are the host key algorithms dash asks for while it only reads a host's key, in
// order of preference: the key it offers to trust is the host's ed25519 key when it has one.
var scanAlgorithms = []string{
	ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

// algorithmsFor are the host key algorithms that make a host present the pinned key's type, so
// that a host with several keys is never taken for one whose key changed.
func algorithmsFor(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{key.Type()}
}

// errScanned ends the handshake of a host without a pinned key once its key is read: no
// credential is ever sent to a host dash has not been told to trust.
var errScanned = errors.New("host key read; the host is not trusted yet")

// errKeyChanged ends the handshake of a host that presents another key than the pinned one.
var errKeyChanged = errors.New("the host presents another key than the pinned one")

// runner is the goroutine of one host: a state machine over sshmodel.HostState that connects,
// reads frames while the host is up, and waits before it tries again.
type runner struct {
	rt     *runtime
	i      int
	host   pluginconf.SSHHost
	addr   string        // host:port as dialled
	pinned ssh.PublicKey // nil: unverified
	log    *slog.Logger
	// logged is the state and reason last logged: a host that keeps failing the same way is
	// logged once, not at every attempt.
	logged string
}

func newRunner(rt *runtime, i int, h pluginconf.SSHHost, pinned ssh.PublicKey) *runner {
	host, port, _ := pluginconf.SplitSSHAddress(h.Address) // Validate has accepted it
	return &runner{
		rt: rt, i: i, host: h, addr: net.JoinHostPort(host, strconv.Itoa(port)), pinned: pinned,
		log: rt.h.Logger().With("host", h.Name),
	}
}

// loop tries the host until ctx ends: each attempt ends in a state, and the state says how long
// to wait before the next one.
func (r *runner) loop(ctx context.Context) {
	tm := r.rt.tm
	retry, auth := tm.retryMin, tm.authFirst
	for ctx.Err() == nil {
		state := r.attempt(ctx)
		var wait time.Duration
		switch state {
		case sshmodel.StateUnreachable:
			wait, retry = jitter(retry), min(retry*2, tm.retryMax)
		case sshmodel.StateUnauthorized:
			wait, auth = auth, min(auth*3/2, tm.retryMax)
		case sshmodel.StateUnverified:
			wait = tm.rescan
		case sshmodel.StateKeyChanged:
			wait = tm.keyChanged
		case sshmodel.StateUnsupported:
			wait = tm.unsupported
		}
		if state != sshmodel.StateUnreachable {
			retry = tm.retryMin
		}
		if state != sshmodel.StateUnauthorized {
			auth = tm.authFirst
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// jitter spreads d by ±20 %, so hosts that failed together do not retry together.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// note records the host's new state and logs it when the state or its reason changed. err is
// scrubbed: it may carry what the network sent.
func (r *runner) note(state sshmodel.HostState, reason string, err error, fn func(s *hostState)) sshmodel.HostState {
	r.rt.update(r.i, func(s *hostState) {
		s.state, s.blocked = state, false
		if fn != nil {
			fn(s)
		}
	})
	key := string(state) + "/" + reason
	if key == r.logged {
		return state
	}
	r.logged = key
	attrs := []any{"state", state, "address", r.addr}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if err != nil {
		attrs = append(attrs, "error", r.rt.h.ScrubLog(err.Error()))
	}
	switch state {
	case sshmodel.StateUp, sshmodel.StateUnverified:
		r.log.Info("ssh host", attrs...)
	default:
		r.log.Warn("ssh host", attrs...)
	}
	return state
}

// attempt connects once and returns the state it ended in. A host that is up stays in here,
// reading frames, until the connection ends or ctx does.
func (r *runner) attempt(ctx context.Context) sshmodel.HostState {
	tm := r.rt.tm
	dctx, cancel := context.WithTimeout(ctx, tm.handshake)
	conn, err := r.rt.h.Dial(dctx, "tcp", r.addr)
	cancel()
	if err != nil {
		if errors.Is(err, plugin.ErrBlockedAddress) {
			return r.note(sshmodel.StateUnreachable, "blocked address", err, func(s *hostState) { s.blocked = true })
		}
		return r.note(sshmodel.StateUnreachable, "dial", err, nil)
	}
	// Stop and the end of the attempt close the connection, whatever is reading it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(tm.handshake))

	var offered ssh.PublicKey
	verified := false
	cfg := &ssh.ClientConfig{
		User:          r.host.User,
		ClientVersion: clientVersion,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			offered = key
			if r.pinned == nil {
				return errScanned
			}
			if !bytes.Equal(key.Marshal(), r.pinned.Marshal()) {
				return errKeyChanged
			}
			verified = true
			return nil
		},
		HostKeyAlgorithms: scanAlgorithms,
	}
	if r.pinned != nil {
		cfg.HostKeyAlgorithms = algorithmsFor(r.pinned)
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(r.rt.signer)}
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, r.addr, cfg)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return sshmodel.StateConnecting
		case r.pinned == nil && offered != nil:
			key := hostKeyOf(offered)
			return r.note(sshmodel.StateUnverified, "", nil, func(s *hostState) { s.offered, s.stats = key, nil })
		case r.pinned != nil && offered != nil && !verified:
			key := hostKeyOf(offered)
			return r.note(sshmodel.StateKeyChanged, "", nil, func(s *hostState) { s.offered, s.stats = key, nil })
		case verified && strings.Contains(err.Error(), "unable to authenticate"):
			return r.note(sshmodel.StateUnauthorized, "", nil, func(s *hostState) { s.stats = nil })
		}
		return r.note(sshmodel.StateUnreachable, "handshake", err, func(s *hostState) { s.stats = nil })
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	_ = conn.SetDeadline(time.Time{})
	return r.session(ctx, client)
}

// read is one frame, or why there is none.
type read struct {
	f   frame
	err error
}

// session runs the collector script and reads its frames until the connection ends.
func (r *runner) session(ctx context.Context, client *ssh.Client) sshmodel.HostState {
	tm := r.rt.tm
	gone := func(reason string, err error) sshmodel.HostState {
		if ctx.Err() != nil {
			return sshmodel.StateConnecting
		}
		return r.note(sshmodel.StateUnreachable, reason, err, func(s *hostState) { s.stats = nil })
	}
	nonce, err := newNonce()
	if err != nil {
		return gone("nonce", err)
	}
	sess, err := client.NewSession()
	if err != nil {
		return gone("session", err)
	}
	defer sess.Close()
	out, err := sess.StdoutPipe()
	if err != nil {
		return gone("session", err)
	}
	secs := intervalSeconds(r.rt.cfg)
	every := inventoryEvery(secs)
	sess.Stdin = strings.NewReader(buildScript(secs, every, nonce))
	if err := sess.Start(remoteCommand); err != nil {
		return gone("session", err)
	}

	done := make(chan struct{})
	defer close(done)
	frames := make(chan read)
	go func() {
		fr := newFrameReader(out, nonce)
		for {
			f, err := fr.next()
			select {
			case frames <- read{f, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go r.keepalive(client, done)

	col := &collector{}
	seq := 0
	deadline := time.NewTimer(r.rt.interval + tm.inventoryGrace)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return sshmodel.StateConnecting
		case <-deadline.C:
			return gone("silence", errors.New("no frame within the deadline"))
		case x := <-frames:
			if x.err != nil {
				if seq == 0 && exited(sess) {
					return r.note(sshmodel.StateUnsupported, "the script did not run", x.err, func(s *hostState) { s.stats = nil })
				}
				if errors.Is(x.err, errProtocol) {
					return gone("protocol violation", x.err)
				}
				return gone("connection lost", x.err)
			}
			if seq == 0 && kernelOf(x.f) != "Linux" {
				return r.note(sshmodel.StateUnsupported, "not Linux", nil, func(s *hostState) { s.stats = nil })
			}
			now := r.rt.h.Now()
			st := col.frame(x.f, now)
			info := col.info()
			r.rt.hist.add(r.host.Name, sampleOf(st))
			r.note(sshmodel.StateUp, "", nil, func(s *hostState) {
				s.offered, s.stats = nil, &st
				if info != nil {
					s.info = info
				}
			})
			seq++
			// The next frame carries the inventory every so often, and its commands may take
			// longer.
			grace := tm.frameGrace
			if seq%every == 0 {
				grace = tm.inventoryGrace
			}
			deadline.Reset(r.rt.interval + grace)
		}
	}
}

// exited reports whether the remote command ended with an exit status of its own: the host ran
// something other than the script (a shell that is not POSIX, a forced command) rather than
// losing the connection.
func exited(sess *ssh.Session) bool {
	res := make(chan error, 1)
	go func() { res <- sess.Wait() }()
	select {
	case err := <-res:
		var exit *ssh.ExitError
		return errors.As(err, &exit) || err == nil
	case <-time.After(2 * time.Second):
		return false
	}
}

// keepalive asks the host for a reply every tm.keepalive; a host that does not answer within
// the same time has its connection closed, which ends the session's reads.
func (r *runner) keepalive(client *ssh.Client, done <-chan struct{}) {
	tick := time.NewTicker(r.rt.tm.keepalive)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}
		res := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			res <- err
		}()
		select {
		case <-done:
			return
		case err := <-res:
			if err != nil && !errors.Is(err, io.EOF) {
				_ = client.Close()
				return
			}
		case <-time.After(r.rt.tm.keepalive):
			_ = client.Close()
			return
		}
	}
}
