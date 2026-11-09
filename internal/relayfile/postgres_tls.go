package relayfile

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"strings"
	"time"

	"github.com/lib/pq"
)

// postgresTLSDialer completes PostgreSQL's SSL negotiation and normal verified
// TLS before pq may send its startup packet. The caller must disable only pq's
// second SSL negotiation after installing this dialer on its private connector.
// No CA filename or ambient TLS configuration is used by this transport.
type postgresTLSDialer struct {
	config  *tls.Config
	timeout time.Duration
}

var _ pq.Dialer = (*postgresTLSDialer)(nil)
var _ pq.DialerContext = (*postgresTLSDialer)(nil)

func newVerifiedPostgresDialer(caPEM []byte, serverName string, timeout time.Duration) (pq.Dialer, error) {
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, postgresRefuse("tls_invalid_timeout")
	}
	if serverName == "" || strings.TrimSpace(serverName) != serverName || strings.ContainsAny(serverName, "\x00\r\n\t /\\") {
		return nil, postgresRefuse("tls_invalid_server_name")
	}
	// Copy before parsing: neither the caller's slice nor a mutable CA file can
	// change the roots used by later pooled connections.
	rest := bytes.TrimSpace(append([]byte(nil), caPEM...))
	roots := x509.NewCertPool()
	count := 0
	for len(rest) > 0 {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, postgresRefuse("tls_invalid_ca")
		}
		block, next := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, postgresRefuse("tls_invalid_ca")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return nil, postgresRefuse("tls_invalid_ca")
		}
		roots.AddCert(certificate)
		count++
		rest = bytes.TrimSpace(next)
	}
	if count == 0 {
		return nil, postgresRefuse("tls_invalid_ca")
	}
	return &postgresTLSDialer{config: &tls.Config{RootCAs: roots, ServerName: serverName, MinVersion: tls.VersionTLS12}, timeout: timeout}, nil
}

func (d *postgresTLSDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *postgresTLSDialer) DialTimeout(network, address string, timeout time.Duration) (net.Conn, error) {
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, postgresRefuse("tls_invalid_timeout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, network, address)
}

func (d *postgresTLSDialer) DialContext(parent context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, postgresRefuse("tls_invalid_network")
	}
	ctx, cancel := context.WithTimeout(parent, d.timeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, postgresRefuse("tls_connection_failed")
	}
	returned := false
	defer func() {
		if !returned {
			_ = raw.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	if raw.SetDeadline(deadline) != nil {
		return nil, postgresRefuse("tls_negotiation_failed")
	}
	// A deadline alone would not interrupt an earlier explicit cancellation
	// while the peer withholds its one-byte SSL response.
	cancelDone := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() { _ = raw.Close(); close(cancelDone) })
	detached := false
	defer func() {
		if !detached && !stopCancellation() {
			<-cancelDone
		}
	}()
	request := make([]byte, 8)
	binary.BigEndian.PutUint32(request[:4], 8)
	binary.BigEndian.PutUint32(request[4:], 80877103)
	if _, err = io.Copy(raw, bytes.NewReader(request)); err != nil {
		return nil, postgresRefuse("tls_negotiation_failed")
	}
	var response [1]byte
	// Read exactly one byte. Any extra server bytes must remain for TLS to
	// authenticate or reject; a buffered reader could discard stuffed bytes.
	if _, err = io.ReadFull(raw, response[:]); err != nil || response[0] != 'S' {
		return nil, postgresRefuse("tls_negotiation_failed")
	}
	verified := tls.Client(raw, d.config.Clone())
	if verified.HandshakeContext(ctx) != nil {
		return nil, postgresRefuse("tls_handshake_failed")
	}
	if !stopCancellation() {
		<-cancelDone
		detached = true
		return nil, postgresRefuse("tls_handshake_failed")
	}
	detached = true
	if ctx.Err() != nil || verified.SetDeadline(time.Time{}) != nil {
		return nil, postgresRefuse("tls_handshake_failed")
	}
	returned = true
	return verified, nil
}
