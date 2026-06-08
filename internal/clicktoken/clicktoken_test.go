package clicktoken_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/hojex/adserver/internal/clicktoken"
)

const testSecret = "test-hmac-secret-not-for-production"

func newTestSigner(t *testing.T) *clicktoken.Signer {
	t.Helper()
	s, err := clicktoken.New(testSecret, clicktoken.DefaultTTL)
	if err != nil {
		t.Fatalf("clicktoken.New: %v", err)
	}
	return s
}

// New with empty secret must fail (fail-closed).
func TestNew_EmptySecret_Fails(t *testing.T) {
	_, err := clicktoken.New("", clicktoken.DefaultTTL)
	if err == nil {
		t.Fatal("expected error for empty secret, got nil")
	}
}

// Sign + Validate round-trip: valid token → 302 path.
func TestSignValidate_ValidToken(t *testing.T) {
	s := newTestSigner(t)
	now := time.Now()
	expiry := now.Add(clicktoken.DefaultTTL)

	tok := s.Sign("dec-001", "ban-001", "https://advertiser.example.com/landing", expiry)
	if tok == "" {
		t.Fatal("Sign returned empty token")
	}

	did, bid, dest, err := s.Validate(tok, now)
	if err != nil {
		t.Fatalf("Validate returned unexpected error: %v", err)
	}
	if did != "dec-001" {
		t.Errorf("decision_id: got %q, want %q", did, "dec-001")
	}
	if bid != "ban-001" {
		t.Errorf("banner_id: got %q, want %q", bid, "ban-001")
	}
	if dest != "https://advertiser.example.com/landing" {
		t.Errorf("dest_url: got %q, want %q", dest, "https://advertiser.example.com/landing")
	}
}

// Expired token → reject with error (4xx path).
func TestValidate_ExpiredToken(t *testing.T) {
	s := newTestSigner(t)
	past := time.Now().Add(-2 * clicktoken.DefaultTTL)
	expiry := past.Add(time.Second) // expiry is in the past relative to now
	tok := s.Sign("dec-002", "ban-002", "https://example.com/ok", expiry)

	_, _, _, err := s.Validate(tok, time.Now())
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

// Tampered HMAC → reject.
func TestValidate_TamperedMAC(t *testing.T) {
	s := newTestSigner(t)
	expiry := time.Now().Add(clicktoken.DefaultTTL)
	tok := s.Sign("dec-003", "ban-003", "https://example.com/ok", expiry)

	// Isolate the MAC segment (after the last ".").
	lastDot := strings.LastIndex(tok, ".")
	macB64 := tok[lastDot+1:]
	if len(macB64) == 0 {
		t.Fatal("token has no MAC portion")
	}

	// Decode → flip byte[0] with XOR 0xFF (guaranteed bit-level corruption,
	// deterministic regardless of base64 alphabet alignment) → re-encode.
	// This avoids the flakiness of mutating a single base64 char: the last
	// char of a 43-char RawURLEncoding MAC encodes only 4 useful bits, so
	// a char swap can silently decode to the same 32-byte value.
	macBytes, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		t.Fatalf("base64 decode of MAC: %v", err)
	}
	macBytes[0] ^= 0xFF
	tamperedMAC := base64.RawURLEncoding.EncodeToString(macBytes)
	tampered := tok[:lastDot+1] + tamperedMAC

	_, _, _, err = s.Validate(tampered, time.Now())
	if err == nil {
		t.Fatal("expected HMAC mismatch error for tampered token, got nil")
	}
}

// Tampered payload (changed dest_url) → reject.
func TestValidate_TamperedPayload(t *testing.T) {
	s := newTestSigner(t)
	expiry := time.Now().Add(clicktoken.DefaultTTL)
	tok := s.Sign("dec-004", "ban-004", "https://good.example.com/", expiry)

	// Replace the payload with a different dest but keep the old MAC.
	lastDot := strings.LastIndex(tok, ".")
	originalMAC := tok[lastDot:]
	// Build a fake payload with malicious URL.
	fakeTok := "dec-004:ban-004:99999999999:https://evil.example.com/" + originalMAC

	_, _, _, err := s.Validate(fakeTok, time.Now())
	if err == nil {
		t.Fatal("expected HMAC mismatch for tampered payload, got nil")
	}
}

// Malformed token (no MAC separator).
func TestValidate_MalformedToken(t *testing.T) {
	s := newTestSigner(t)

	_, _, _, err := s.Validate("nodotinhere", time.Now())
	if err == nil {
		t.Fatal("expected error for token with no MAC separator")
	}
}

// Empty token → reject.
func TestValidate_EmptyToken(t *testing.T) {
	s := newTestSigner(t)
	_, _, _, err := s.Validate("", time.Now())
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

// Token with dest_url containing ":" characters round-trips correctly.
func TestSignValidate_DestURLWithColons(t *testing.T) {
	s := newTestSigner(t)
	dest := "https://example.com/path?foo=bar&baz=qux"
	expiry := time.Now().Add(clicktoken.DefaultTTL)

	tok := s.Sign("dec-005", "ban-005", dest, expiry)
	_, _, gotDest, err := s.Validate(tok, time.Now())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if gotDest != dest {
		t.Errorf("dest_url round-trip: got %q, want %q", gotDest, dest)
	}
}

// Different secret → HMAC mismatch.
func TestValidate_WrongSecret(t *testing.T) {
	signer1 := newTestSigner(t)
	signer2, err := clicktoken.New("different-secret-abc", clicktoken.DefaultTTL)
	if err != nil {
		t.Fatalf("clicktoken.New: %v", err)
	}

	expiry := time.Now().Add(clicktoken.DefaultTTL)
	tok := signer1.Sign("dec-006", "ban-006", "https://example.com/", expiry)

	_, _, _, err = signer2.Validate(tok, time.Now())
	if err == nil {
		t.Fatal("expected HMAC mismatch when validating with wrong secret")
	}
}
