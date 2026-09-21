// Package auth exchanges a Datum Cloud service-account key for an access
// token. It follows the same flow as `datumctl login --credentials`: discover
// the token endpoint over OIDC, sign a short-lived RS256 assertion with the
// service account's private key, and trade it through the jwt-bearer grant.
//
// Service-account sessions carry no refresh token, so an expired token is
// replaced by signing a fresh assertion.
package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	credentialsType = "datum_service_account"
	defaultScope    = "openid profile email offline_access"
	jwtBearerGrant  = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	assertionTTL    = 60 * time.Second
	// Tokens are renewed this long before they expire so an in-flight request
	// never carries a token that lapses on the way.
	expiryLeeway = 30 * time.Second
)

// Credentials is the service-account key JSON downloaded from Datum Cloud.
type Credentials struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientEmail  string `json:"client_email"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	Scope        string `json:"scope"`
}

// LoadCredentials reads and validates a service-account key file.
func LoadCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials %q: %w", path, err)
	}
	return ParseCredentials(data)
}

// ParseCredentials validates a service-account key JSON document.
func ParseCredentials(data []byte) (*Credentials, error) {
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	if c.Type != "" && c.Type != credentialsType {
		return nil, fmt.Errorf("unsupported credentials type %q, want %q", c.Type, credentialsType)
	}
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if c.PrivateKeyID == "" {
		missing = append(missing, "private_key_id")
	}
	if c.PrivateKey == "" {
		missing = append(missing, "private_key")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("credentials missing %s", strings.Join(missing, ", "))
	}
	return &c, nil
}

// ParseRSAPrivateKey accepts a PEM key in PKCS#1 or PKCS#8 form, since the
// portal has issued both.
func ParseRSAPrivateKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key (tried PKCS#1 and PKCS#8): %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not an RSA key")
	}
	return key, nil
}

// Audience returns the scheme and host of the token endpoint. The auth server
// rejects assertions whose audience is the full endpoint URL.
func Audience(tokenEndpoint string) (string, error) {
	u, err := url.Parse(tokenEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse token endpoint: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("token endpoint %q is not an absolute URL", tokenEndpoint)
	}
	return u.Scheme + "://" + u.Host, nil
}

// SignAssertion builds the RS256 JWT used in the jwt-bearer grant.
func SignAssertion(c *Credentials, audience string, now time.Time) (string, error) {
	key, err := ParseRSAPrivateKey(c.PrivateKey)
	if err != nil {
		return "", err
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", fmt.Errorf("generate jti: %w", err)
	}

	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": c.PrivateKeyID}
	claims := map[string]any{
		"iss": c.ClientID,
		"sub": c.ClientID,
		"aud": audience,
		"jti": hex.EncodeToString(jti),
		"iat": now.Unix(),
		"exp": now.Add(assertionTTL).Unix(),
	}

	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64(h) + "." + b64(p)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign assertion: %w", err)
	}
	return signingInput + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// TokenSource caches an access token and re-signs an assertion when it expires.
type TokenSource struct {
	creds   *Credentials
	authURL string
	client  *http.Client
	now     func() time.Time

	mu            sync.Mutex
	tokenEndpoint string
	token         string
	expiry        time.Time
}

// NewTokenSource returns a TokenSource for the auth server at authURL, for
// example https://auth.example.com.
func NewTokenSource(creds *Credentials, authURL string, client *http.Client) *TokenSource {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &TokenSource{
		creds:   creds,
		authURL: strings.TrimRight(authURL, "/"),
		client:  client,
		now:     time.Now,
	}
}

// Token returns a valid access token, minting a new one when needed.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Add(expiryLeeway).Before(s.expiry) {
		return s.token, nil
	}
	if s.tokenEndpoint == "" {
		endpoint, err := s.discover(ctx)
		if err != nil {
			return "", err
		}
		s.tokenEndpoint = endpoint
	}
	aud, err := Audience(s.tokenEndpoint)
	if err != nil {
		return "", err
	}
	assertion, err := SignAssertion(s.creds, aud, s.now())
	if err != nil {
		return "", err
	}
	token, ttl, err := s.exchange(ctx, assertion)
	if err != nil {
		return "", err
	}
	s.token = token
	s.expiry = s.now().Add(ttl)
	return token, nil
}

func (s *TokenSource) discover(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.authURL+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc discovery: %s", resp.Status)
	}
	var doc struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	if doc.TokenEndpoint == "" {
		return "", errors.New("oidc discovery: no token_endpoint")
	}
	return doc.TokenEndpoint, nil
}

func (s *TokenSource) exchange(ctx context.Context, assertion string) (string, time.Duration, error) {
	scope := s.creds.Scope
	if scope == "" {
		scope = defaultScope
	}
	form := url.Values{
		"grant_type": {jwtBearerGrant},
		"assertion":  {assertion},
		"scope":      {scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("token exchange: %s: %w", resp.Status, err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		if tr.Error != "" {
			return "", 0, fmt.Errorf("token exchange: %s (%s)", tr.Error, tr.ErrorDesc)
		}
		return "", 0, fmt.Errorf("token exchange: %s", resp.Status)
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return tr.AccessToken, ttl, nil
}
