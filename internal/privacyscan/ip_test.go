package privacyscan

import (
	"reflect"
	"testing"
)

// TestFindIPLiterals_Table is the DEFAULT-DENY regression table for the
// scanner. It exists because a prior implementation used
// strings.Trim(cand, ".:") to clean up a maximally-greedy candidate before
// calling net.ParseIP — and that trim silently decapitates the leading/
// trailing ':' that makes compressed IPv6 syntax valid in the first place.
// The three cases marked CRITICAL below reproduce exactly that bug:
//   - "::1"                 -> Trim strips the leading "::"  -> "1"
//   - "2001:db8::"          -> Trim strips the trailing "::" -> "2001:db8"
//   - "::ffff:203.0.113.5"  -> Trim strips the leading "::"  -> "ffff:203.0.113.5"
//
// All three are rejected by net.ParseIP once mangled, so the old
// implementation reports ZERO matches for them (a false negative). The
// third form ("::ffff:a.b.c.d", IPv4-mapped IPv6) is the most operationally
// dangerous: it is exactly the textual form Go's own dual-stack sockets
// produce for a client's remote address, so the previous scanner missed the
// single most likely real-world shape of a leaked client IP.
func TestFindIPLiterals_Table(t *testing.T) {
	cases := []struct {
		name string
		data string
		want []string
	}{
		// --- positives -------------------------------------------------
		{
			name: "ipv4 inside JSON custom_vars value",
			data: `{"zone_id":"z1","custom_vars":{"debug":"203.0.113.5"}}`,
			want: []string{"203.0.113.5"},
		},
		{
			name: "ipv4 after key=value with no surrounding quotes",
			data: "cliente=203.0.113.5",
			want: []string{"203.0.113.5"},
		},
		{
			name: "ipv4 embedded in protobuf-ish binary framing",
			data: string(append(append([]byte{0x0A, 0x0B}, []byte("203.0.113.9")...), 0x12, 0x03)),
			want: []string{"203.0.113.9"},
		},
		{
			name: "ipv6 uncompressed link-local",
			data: "fe80::1",
			want: []string{"fe80::1"},
		},
		{
			name: "ipv6 compressed inside quoted field",
			data: `referer_url=https://x.example.com geo="2001:db8::1"`,
			want: []string{"2001:db8::1"},
		},
		{
			name: "CRITICAL: ipv6 loopback, leading '::' must survive",
			data: "::1",
			want: []string{"::1"},
		},
		{
			name: "CRITICAL: ipv6 with trailing '::' compression must survive",
			data: "2001:db8::",
			want: []string{"2001:db8::"},
		},
		{
			name: "CRITICAL: ipv4-mapped ipv6 (dual-stack socket form) must survive",
			data: "::ffff:203.0.113.5",
			want: []string{"::ffff:203.0.113.5"},
		},
		{
			name: "ipv6 link-local with RFC 4007 zone identifier",
			data: "fe80::1%eth0",
			want: []string{"fe80::1%eth0"},
		},
		{
			name: "ipv4-mapped ipv6 with surrounding prose",
			data: "client remote addr was ::ffff:198.51.100.7 on the wire",
			want: []string{"::ffff:198.51.100.7"},
		},
		{
			name: "sub-candidate via suffix trimming: leading header-style colon is not part of the IP",
			data: "X-Forwarded-For:203.0.113.5",
			want: []string{"203.0.113.5"},
		},
		{
			name: "two distinct literals keep left-to-right, first-seen order",
			data: "a=203.0.113.5 b=198.51.100.7 c=203.0.113.5",
			want: []string{"203.0.113.5", "198.51.100.7"},
		},

		// --- negatives (must NOT be flagged) -----------------------------
		{
			name: "ClickHouse DateTime64 fragment",
			data: "15:04:05.000",
			want: nil,
		},
		{
			name: "RFC3339 timestamp",
			data: "2026-07-25T10:00:00.000Z",
			want: nil,
		},
		{
			name: "schema_version",
			data: "1.0.0",
			want: nil,
		},
		{
			name: "ULID (ImportantAdRequest decision_id)",
			data: "01HZXYZQRS1234567890ABCDE",
			want: nil,
		},
		{
			name: "truncated Chrome UA version (3 octets, not 4)",
			data: "124.0.0",
			want: nil,
		},
		{
			name: "uaClass",
			data: "chrome-desktop",
			want: nil,
		},
		{
			name: "sanitized referer URL",
			data: "https://news.example.com/article",
			want: nil,
		},
		{
			name: "bare unspecified-address token '::' isolated by word boundaries (scope-resolution/YAML/Markdown noise, not a client identifier)",
			data: "priority :: fallback",
			want: nil,
		},
		{
			name: "bare unspecified-address token '::' alone",
			data: "::",
			want: nil,
		},
		{
			name: "clean input with no IP-shaped substring at all",
			data: "no ip here",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FindIPLiterals([]byte(tc.data))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("FindIPLiterals(%q) = %v, want %v", tc.data, got, tc.want)
			}
		})
	}
}

// TestFindIPLiterals_Deterministic asserts that repeated scans of the same
// input always produce the same output, in the same order (DA-9-adjacent
// requirement: forensic/privacy gates must be reproducible, not a function
// of Go map iteration order or similar nondeterminism).
func TestFindIPLiterals_Deterministic(t *testing.T) {
	data := []byte(`ip1=203.0.113.5 ip2=::ffff:198.51.100.7 ip3=2001:db8::1 ip4=fe80::1%eth0`)
	first := FindIPLiterals(data)
	for i := 0; i < 20; i++ {
		got := FindIPLiterals(data)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("FindIPLiterals not deterministic: run 0 = %v, run %d = %v", first, i+1, got)
		}
	}
	want := []string{"203.0.113.5", "::ffff:198.51.100.7", "2001:db8::1", "fe80::1%eth0"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("FindIPLiterals = %v, want %v", first, want)
	}
}

func TestContainsIPLiteral(t *testing.T) {
	if ContainsIPLiteral([]byte("no ip here")) {
		t.Error("ContainsIPLiteral: false positive on clean input")
	}
	if !ContainsIPLiteral([]byte("client=203.0.113.5")) {
		t.Error("ContainsIPLiteral: false negative on embedded IPv4")
	}
	if !ContainsIPLiteral([]byte("client=::ffff:203.0.113.5")) {
		t.Error("ContainsIPLiteral: false negative on IPv4-mapped IPv6 (dual-stack socket form)")
	}
}
