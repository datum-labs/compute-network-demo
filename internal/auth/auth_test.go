package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func pkcs1PEM(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func pkcs8PEM(t *testing.T, key *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// decodeAssertion verifies the signature and returns header and claims.
func decodeAssertion(t *testing.T, jwt string, pub *rsa.PublicKey) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion has %d parts, want 3", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	var header, claims map[string]any
	for i, dst := range []*map[string]any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatal(err)
		}
	}
	return header, claims
}

func TestSignAssertion(t *testing.T) {
	key := testKey(t)
	now := time.Unix(1_800_000_000, 0)

	for name, pemKey := range map[string]string{"pkcs1": pkcs1PEM(key), "pkcs8": pkcs8PEM(t, key)} {
		t.Run(name, func(t *testing.T) {
			creds := &Credentials{ClientID: "338412345", PrivateKeyID: "key-1", PrivateKey: pemKey}
			jwt, err := SignAssertion(creds, "https://auth.example.com", now)
			if err != nil {
				t.Fatal(err)
			}
			header, claims := decodeAssertion(t, jwt, &key.PublicKey)

			if header["alg"] != "RS256" || header["kid"] != "key-1" || header["typ"] != "JWT" {
				t.Errorf("header = %v", header)
			}
			if claims["iss"] != "338412345" || claims["sub"] != "338412345" {
				t.Errorf("iss/sub = %v/%v", claims["iss"], claims["sub"])
			}
			if claims["aud"] != "https://auth.example.com" {
				t.Errorf("aud = %v", claims["aud"])
			}
			if claims["iat"].(float64) != float64(now.Unix()) || claims["exp"].(float64) != float64(now.Unix()+60) {
				t.Errorf("iat/exp = %v/%v", claims["iat"], claims["exp"])
			}
			if jti, _ := claims["jti"].(string); len(jti) < 16 {
				t.Errorf("jti = %q, want random identifier", jti)
			}
		})
	}
}

func TestSignAssertionUniqueJTI(t *testing.T) {
	key := testKey(t)
	creds := &Credentials{ClientID: "c", PrivateKeyID: "k", PrivateKey: pkcs1PEM(key)}
	a, _ := SignAssertion(creds, "https://a", time.Now())
	b, _ := SignAssertion(creds, "https://a", time.Now())
	_, ca := decodeAssertion(t, a, &key.PublicKey)
	_, cb := decodeAssertion(t, b, &key.PublicKey)
	if ca["jti"] == cb["jti"] {
		t.Fatal("jti repeated across assertions")
	}
}

func TestAudience(t *testing.T) {
	got, err := Audience("https://auth.example.com/oauth/v2/token")
	if err != nil || got != "https://auth.example.com" {
		t.Fatalf("Audience = %q, %v", got, err)
	}
	if _, err := Audience("/oauth/v2/token"); err == nil {
		t.Fatal("expected error for relative URL")
	}
}

func TestParseCredentials(t *testing.T) {
	if _, err := ParseCredentials([]byte(`{"type":"datum_service_account","client_id":"a"}`)); err == nil ||
		!strings.Contains(err.Error(), "private_key_id, private_key") {
		t.Fatalf("missing fields error = %v", err)
	}
	if _, err := ParseCredentials([]byte(`{"type":"gcp","client_id":"a","private_key_id":"b","private_key":"c"}`)); err == nil {
		t.Fatal("expected unsupported type error")
	}
}

func TestTokenSourceFlow(t *testing.T) {
	key := testKey(t)
	var srvURL string
	var exchanges atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": srvURL + "/oauth/v2/token"})
		case "/oauth/v2/token":
			exchanges.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != jwtBearerGrant {
				http.Error(w, "bad grant", http.StatusBadRequest)
				return
			}
			if r.Form.Get("scope") != "openid urn:zitadel:iam:org:project:id:zitadel:aud" {
				http.Error(w, "bad scope "+r.Form.Get("scope"), http.StatusBadRequest)
				return
			}
			_, claims := decodeAssertion(t, r.Form.Get("assertion"), &key.PublicKey)
			if claims["aud"] != srvURL {
				http.Error(w, "bad aud", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	creds := &Credentials{ClientID: "c", PrivateKeyID: "k", PrivateKey: pkcs8PEM(t, key),
		Scope: "openid urn:zitadel:iam:org:project:id:zitadel:aud"}
	ts := NewTokenSource(creds, srv.URL, srv.Client())

	for range 3 {
		tok, err := ts.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if tok != "tok-1" {
			t.Fatalf("token = %q", tok)
		}
	}
	if n := exchanges.Load(); n != 1 {
		t.Fatalf("exchanges = %d, want cached token reused", n)
	}

	// Past expiry the source signs a fresh assertion.
	ts.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := ts.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := exchanges.Load(); n != 2 {
		t.Fatalf("exchanges = %d, want re-exchange after expiry", n)
	}
}
