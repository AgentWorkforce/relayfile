package httpapi

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const testBearerJWTKID = "test-bearer-kid"

var testBearerPrivateKey *rsa.PrivateKey

func TestMain(m *testing.M) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	testBearerPrivateKey = privateKey

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jwksDocument{
			Keys: []jwkKey{mustRSATestJWK(testBearerJWTKID, &privateKey.PublicKey)},
		})
	}))

	prevDefaultJWKSURL := defaultRelayAuthJWKSURL
	defaultRelayAuthJWKSURL = jwksServer.URL

	code := m.Run()

	defaultRelayAuthJWKSURL = prevDefaultJWKSURL
	jwksServer.Close()
	os.Exit(code)
}
