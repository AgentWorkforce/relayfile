package relayfile

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lib/pq"
)

const postgresTLSFixtureName = "postgres.fixture.invalid"

func postgresTLSTestCertificates(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Owned test CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{postgresTLSFixtureName},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

// The fixture implements only SSLRequest and TLS, never PostgreSQL auth or a
// real database. All sockets are owned IPv4 loopback listeners.
func postgresTLSTestPeer(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var request [8]byte
		if _, err = io.ReadFull(conn, request[:]); err != nil {
			return
		}
		if binary.BigEndian.Uint32(request[:4]) != 8 || binary.BigEndian.Uint32(request[4:]) != 80877103 {
			t.Error("client did not send exactly PostgreSQL SSLRequest first")
			return
		}
		handler(conn)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("owned TLS test peer did not terminate")
		}
	})
	return listener.Addr().String()
}

func postgresTLSVerifiedPeer(t *testing.T, certificate tls.Certificate, maxVersion uint16) string {
	t.Helper()
	return postgresTLSTestPeer(t, func(raw net.Conn) {
		if _, err := raw.Write([]byte{'S'}); err != nil {
			return
		}
		server := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS10, MaxVersion: maxVersion})
		if server.Handshake() != nil {
			return
		}
		_, _ = server.Write([]byte{0x7f})
	})
}

func postgresTLSConnect(t *testing.T, roots []byte, host, address string) (net.Conn, error) {
	t.Helper()
	dialer, err := newVerifiedPostgresDialer(roots, host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	contextDialer, ok := dialer.(pq.DialerContext)
	if !ok {
		t.Fatal("verified dialer lost context support")
	}
	return contextDialer.DialContext(context.Background(), "tcp", address)
}

func TestVerifiedPostgresTLSReturnsOnlyVerifiedTLSConnection(t *testing.T) {
	ca, certificate := postgresTLSTestCertificates(t)
	address := postgresTLSVerifiedPeer(t, certificate, 0)
	conn, err := postgresTLSConnect(t, ca, postgresTLSFixtureName, address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	secured, ok := conn.(*tls.Conn)
	if !ok || !secured.ConnectionState().HandshakeComplete || secured.ConnectionState().Version < tls.VersionTLS12 || len(secured.ConnectionState().VerifiedChains) == 0 {
		t.Fatal("returned connection was not authenticated TLS >= 1.2")
	}
	var value [1]byte
	if _, err := io.ReadFull(conn, value[:]); err != nil || value[0] != 0x7f {
		t.Fatal("verified connection was unusable after return")
	}
}

func TestVerifiedPostgresTLSWrongCAHostnameAndOldVersionRefused(t *testing.T) {
	ca, certificate := postgresTLSTestCertificates(t)
	wrongCA, _ := postgresTLSTestCertificates(t)
	for _, test := range []struct {
		name string
		ca   []byte
		host string
		max  uint16
	}{{"wrong_ca", wrongCA, postgresTLSFixtureName, 0}, {"wrong_host", ca, "other.fixture.invalid", 0}, {"old_tls", ca, postgresTLSFixtureName, tls.VersionTLS11}} {
		t.Run(test.name, func(t *testing.T) {
			conn, err := postgresTLSConnect(t, test.ca, test.host, postgresTLSVerifiedPeer(t, certificate, test.max))
			if conn != nil || err == nil || err.Error() != "postgres_tls_handshake_failed" {
				t.Fatalf("TLS refusal did not return a safe classified error: %v", err)
			}
		})
	}
}

func TestVerifiedPostgresTLSNoPlaintextFallbackOrServerErrorEcho(t *testing.T) {
	ca, _ := postgresTLSTestCertificates(t)
	for _, response := range [][]byte{{'N'}, {'X'}, append([]byte{'E'}, []byte("untrusted server message")...)} {
		t.Run(string(response[:1]), func(t *testing.T) {
			noStartup := make(chan bool, 1)
			address := postgresTLSTestPeer(t, func(raw net.Conn) {
				_, _ = raw.Write(response)
				var data [1]byte
				n, err := raw.Read(data[:])
				noStartup <- n == 0 && (errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET))
			})
			conn, err := postgresTLSConnect(t, ca, postgresTLSFixtureName, address)
			if conn != nil || err == nil || err.Error() != "postgres_tls_negotiation_failed" || strings.Contains(err.Error(), "untrusted") {
				t.Fatal("unauthenticated SSL response escaped or was accepted")
			}
			select {
			case empty := <-noStartup:
				if !empty {
					t.Fatal("startup/plaintext bytes were sent after SSL refusal")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("refused socket was not closed")
			}
		})
	}
}

func TestVerifiedPostgresTLSStuffedResponseNotDiscarded(t *testing.T) {
	ca, _ := postgresTLSTestCertificates(t)
	address := postgresTLSTestPeer(t, func(raw net.Conn) {
		_, _ = raw.Write([]byte{'S', 0, 0, 0, 0, 0})
		_, _ = io.Copy(io.Discard, raw)
	})
	conn, err := postgresTLSConnect(t, ca, postgresTLSFixtureName, address)
	if conn != nil || err == nil || err.Error() != "postgres_tls_handshake_failed" {
		t.Fatal("extra unauthenticated response bytes were discarded or accepted")
	}
}

func TestVerifiedPostgresTLSCancellationAndDeadlineBoundNegotiation(t *testing.T) {
	ca, _ := postgresTLSTestCertificates(t)
	for _, cancelEarly := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "timeout"}[cancelEarly], func(t *testing.T) {
			requested := make(chan struct{})
			address := postgresTLSTestPeer(t, func(raw net.Conn) { close(requested); _, _ = io.Copy(io.Discard, raw) })
			duration := time.Second
			if !cancelEarly {
				duration = 40 * time.Millisecond
			}
			dialer, err := newVerifiedPostgresDialer(ca, postgresTLSFixtureName, duration)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				conn, err := dialer.(pq.DialerContext).DialContext(ctx, "tcp", address)
				if conn != nil {
					conn.Close()
				}
				result <- err
			}()
			select {
			case <-requested:
			case err := <-result:
				t.Fatalf("connection ended before SSL request: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("owned peer did not receive SSL request")
			}
			if cancelEarly {
				cancel()
			}
			select {
			case err := <-result:
				if err == nil || err.Error() != "postgres_tls_negotiation_failed" {
					t.Fatal("blocked negotiation was not refused safely")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation/deadline did not bound SSL response wait")
			}
		})
	}
}

func TestVerifiedPostgresTLSCapturedRootsIgnoreLaterSliceAndFileChanges(t *testing.T) {
	ca, originalCertificate := postgresTLSTestCertificates(t)
	otherCA, otherCertificate := postgresTLSTestCertificates(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca, 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dialer, err := newVerifiedPostgresDialer(captured, postgresTLSFixtureName, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := range captured {
		captured[i] = 'X'
	}
	if err := os.WriteFile(path, otherCA, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		certificate tls.Certificate
		allowed     bool
	}{{originalCertificate, true}, {otherCertificate, false}} {
		conn, err := dialer.(pq.DialerContext).DialContext(context.Background(), "tcp", postgresTLSVerifiedPeer(t, test.certificate, 0))
		if test.allowed {
			if err != nil || conn == nil {
				t.Fatal("captured original roots stopped authenticating original peer")
			}
			conn.Close()
		} else if conn != nil || err == nil {
			t.Fatal("later file/slice changed captured trust roots")
		}
	}
}

func TestVerifiedPostgresTLSInvalidInputsRefuseBeforeConnection(t *testing.T) {
	ca, leaf := postgresTLSTestCertificates(t)
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]})
	for _, invalid := range [][]byte{nil, []byte("not PEM"), append(bytes.Clone(ca), []byte("junk")...), append(bytes.Clone(ca), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})...), leafPEM} {
		if dialer, err := newVerifiedPostgresDialer(invalid, postgresTLSFixtureName, time.Second); dialer != nil || err == nil || err.Error() != "postgres_tls_invalid_ca" {
			t.Fatal("invalid root data admitted")
		}
	}
	for _, duration := range []time.Duration{0, -time.Second, 31 * time.Second} {
		if dialer, err := newVerifiedPostgresDialer(ca, postgresTLSFixtureName, duration); dialer != nil || err == nil {
			t.Fatal("invalid timeout admitted")
		}
	}
	for _, host := range []string{"", " bad", "host\nname", "https://host"} {
		if dialer, err := newVerifiedPostgresDialer(ca, host, time.Second); dialer != nil || err == nil {
			t.Fatal("invalid TLS name admitted")
		}
	}
	dialer, _ := newVerifiedPostgresDialer(ca, postgresTLSFixtureName, time.Second)
	if conn, err := dialer.Dial("unix", "/unused"); conn != nil || err == nil {
		t.Fatal("unix transport admitted")
	}
	for _, duration := range []time.Duration{0, 31 * time.Second} {
		if conn, err := dialer.DialTimeout("tcp", "127.0.0.1:1", duration); conn != nil || err == nil || err.Error() != "postgres_tls_invalid_timeout" {
			t.Fatal("invalid per-call timeout admitted")
		}
	}
}
