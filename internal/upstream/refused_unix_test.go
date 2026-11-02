//go:build unix

package upstream

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
)

// refusedURL is the URL of a loopback port that refuses connections, for as long as the test
// runs. The port is bound by a socket that never listens: the kernel answers a connect to a
// bound, non-listening port with a reset (connection refused), and while the socket is held no
// other test server can be given the port, which a port freed by closing a listener could be.
func refusedURL(t *testing.T) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
}

// The port refuses connections and no listener can take it while the test holds it: the two
// properties TestCategory relies on.
func TestRefusedURLRefusesAndCannotBeTaken(t *testing.T) {
	u := refusedURL(t)
	addr := strings.TrimPrefix(u, "http://")
	_, err := net.Dial("tcp", addr)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("dial %s: %v, want connection refused", addr, err)
	}
	if l, err := net.Listen("tcp", addr); err == nil {
		l.Close()
		t.Fatalf("a listener took %s while the test holds it", addr)
	}
}
