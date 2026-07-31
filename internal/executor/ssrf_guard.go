package executor

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// An inline http_api task runs in the control-plane process (ADR 0021/0031), so
// its outbound request is made with the control plane's network reach. Without a
// guard, a DAG author (write:dag) could point it at the cloud metadata endpoint
// (169.254.169.254), the kube-apiserver, or any in-cluster/private service, and
// read the response back as XCom — a server-side request forgery. These two
// guards close that: only http/https schemes, and a dialer that refuses to
// connect to a private, loopback, or link-local address. The dial check runs on
// the RESOLVED ip at connect time, so it also defeats DNS rebinding and follows
// through redirects (each redirect re-dials through the same transport).
var (
	// ErrBlockedAddress reports a dial to a private/loopback/link-local address.
	ErrBlockedAddress = errors.New("blocked address (SSRF guard)")
	// ErrBlockedScheme reports a non-http(s) request URL.
	ErrBlockedScheme = errors.New("blocked url scheme (SSRF guard)")
)

// deniedIP reports whether dialing ip would reach an address an inline task must
// not: loopback, link-local (cloud metadata lives at 169.254.169.254), private
// (RFC1918 and IPv6 unique-local), or the unspecified address.
func deniedIP(ip net.IP) bool {
	return ip == nil ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified()
}

// guardedDialControl is a net.Dialer.Control hook. address is host:port with the
// host already resolved to an IP literal, so checking it here is rebinding-safe:
// the value checked is the value dialed.
func guardedDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if deniedIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, host)
	}
	return nil
}

// allowedScheme rejects any request URL that is not http or https, before a
// request is built. It blocks file://, gopher://, and similar exfiltration or
// local-file schemes.
func allowedScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: unparseable url: %w", ErrBlockedScheme, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q", ErrBlockedScheme, u.Scheme)
	}
	return nil
}

// newGuardedHTTPClient builds the http.Client the inline executor uses by
// default: an ordinary transport whose dialer refuses private/loopback/link-local
// destinations via guardedDialControl.
func newGuardedHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: guardedDialControl}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}
