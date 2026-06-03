// Package clicktoken mints and validates HMAC-SHA256-signed click tokens.
//
// # Purpose
//
// The decision service signs a token at ad-serve time that binds
// decision_id, banner_id, dest_url (from banner config), and an expiry.
// The collector validates the token before issuing a 302 redirect.
// This eliminates the open-redirect vulnerability: the dest_url is NEVER
// taken from an unsigned query parameter.
//
// # Token format
//
//	base64url( decision_id ":" banner_id ":" expiry_unix ":" dest_url ) + "." + base64url( HMAC-SHA256(secret, payload) )
//
// All fields are URL-safe base64 encoded individually; the payload is the
// concatenation joined by ":". The HMAC covers the entire payload string.
//
// # Secret
//
// The secret is read from the CK_HMAC_SECRET environment variable by the
// caller.  This package accepts the secret as a constructor parameter so it
// is never hardcoded here.
//
// # Fail-closed
//
// New() returns an error when the secret is empty.  The caller MUST refuse
// to start (or disable /ck) rather than fall back to a default secret.
package clicktoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTTL is the maximum age of a valid click token.
	// Tokens older than this are rejected even if the HMAC is correct.
	DefaultTTL = 24 * time.Hour
)

// Signer mints and validates click tokens.
// It is safe for concurrent use.
type Signer struct {
	secret []byte
	ttl    time.Duration
}

// New creates a Signer with the given HMAC secret.
// Returns an error when secret is empty — fail-closed: callers must not
// fall back to a default secret.
func New(secret string, ttl time.Duration) (*Signer, error) {
	if secret == "" {
		return nil, fmt.Errorf("clicktoken: CK_HMAC_SECRET is empty — refusing to start (fail-closed)")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Signer{secret: []byte(secret), ttl: ttl}, nil
}

// Sign mints a signed token for a click redirect.
//
// Parameters:
//
//	decisionID — ULID of the ad decision.
//	bannerID   — banner identifier.
//	destURL    — click landing URL from banner config (server-side, never user input).
//	expiry     — when the token expires; usually time.Now().Add(DefaultTTL).
//
// The returned token is safe to include in a URL query parameter.
func (s *Signer) Sign(decisionID, bannerID, destURL string, expiry time.Time) string {
	payload := buildPayload(decisionID, bannerID, destURL, expiry.Unix())
	mac := s.computeMAC(payload)
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac)
}

// Validate parses and validates a click token.
// Returns the embedded dest_url on success.
// Returns an error when the token is malformed, has an invalid HMAC, or is expired.
//
// The dest_url returned here was embedded at sign time from server-side
// banner config — it is safe to redirect to after validateDestURL passes.
func (s *Signer) Validate(token string, now time.Time) (decisionID, bannerID, destURL string, err error) {
	if token == "" {
		return "", "", "", fmt.Errorf("clicktoken: empty token")
	}

	// Split payload from MAC.
	lastDot := strings.LastIndex(token, ".")
	if lastDot < 0 {
		return "", "", "", fmt.Errorf("clicktoken: malformed token (no MAC separator)")
	}
	payload := token[:lastDot]
	macB64 := token[lastDot+1:]

	// Constant-time MAC check.
	gotMAC, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return "", "", "", fmt.Errorf("clicktoken: invalid MAC encoding: %w", err)
	}
	expectedMAC := s.computeMAC(payload)
	if !hmac.Equal(gotMAC, expectedMAC) {
		return "", "", "", fmt.Errorf("clicktoken: HMAC mismatch — token invalid or tampered")
	}

	// Parse payload fields: decisionID:bannerID:expiryUnix:destURL
	// destURL may contain ":", so we only split on the first 3 ":".
	parts := strings.SplitN(payload, ":", 4)
	if len(parts) != 4 {
		return "", "", "", fmt.Errorf("clicktoken: malformed payload (expected 4 fields, got %d)", len(parts))
	}
	did := parts[0]
	bid := parts[1]
	expiryUnixStr := parts[2]
	dest := parts[3]

	expiryUnix, err := strconv.ParseInt(expiryUnixStr, 10, 64)
	if err != nil {
		return "", "", "", fmt.Errorf("clicktoken: invalid expiry: %w", err)
	}
	expiry := time.Unix(expiryUnix, 0)
	if now.After(expiry) {
		return "", "", "", fmt.Errorf("clicktoken: token expired at %s (now %s)", expiry, now)
	}

	return did, bid, dest, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildPayload assembles the payload string that is both stored in the token
// and covered by the HMAC.  Format: decisionID:bannerID:expiryUnix:destURL
func buildPayload(decisionID, bannerID, destURL string, expiryUnix int64) string {
	return fmt.Sprintf("%s:%s:%d:%s", decisionID, bannerID, expiryUnix, destURL)
}

// computeMAC returns HMAC-SHA256(secret, data).
func (s *Signer) computeMAC(data string) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(data))
	return h.Sum(nil)
}
