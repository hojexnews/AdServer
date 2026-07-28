// Package privacyscan provides a DEFAULT-DENY scanner for IP-address
// literals (IPv4/IPv6) embedded anywhere in an arbitrary byte blob.
//
// # Why whole-blob, not field-by-field (TX-5/DA-11, PRIV-02)
//
// The collector is the sole component allowed to see the client's raw IP
// (services/collector/cmd/collector/main.go: resolveAndDiscardIP). Every
// downstream artifact — telemetry events serialised to the WAL/Redpanda,
// and structured logs — MUST NEVER carry that IP, in ANY field, including
// free-form maps such as an AdRequest's custom_vars (publisher-supplied
// key/value pairs the collector does not otherwise validate).
//
// A gate that enumerates "the fields that must not contain an IP" recreates
// the exact scope-narrowing bug it exists to prevent: it silently misses new
// fields, map entries, or log attributes added later. FindIPLiterals instead
// treats the ENTIRE serialized representation as untrusted and reports any
// substring that PARSES as a syntactically valid IP address, regardless of
// which field, map key, or log attribute produced it.
package privacyscan

import (
	"net/netip"
	"regexp"
)

// ipRun matches maximal runs of hex digits, ':' and '.' — the characters
// that can appear in the "core" of an IPv4 or IPv6 textual address — with an
// OPTIONAL trailing zone identifier ("%eth0", RFC 4007 / RFC 6874) as used
// by link-local IPv6 literals such as "fe80::1%eth0". The pattern is
// intentionally permissive (it also matches things that are NOT IP
// addresses, such as "15:04:05" or "1.0.0"): every sub-candidate generated
// from a run (see FindIPLiterals) is subsequently validated with
// netip.ParseAddr, which is strict about structure and — unlike
// net.ParseIP — understands zone identifiers. This candidate+validate
// design avoids both false negatives (a naively-anchored regex would miss
// "::"-compressed IPv6, or the IPv4-mapped form "::ffff:a.b.c.d" that Go's
// own dual-stack sockets hand back for the client's remote address) and
// false positives (a loose regex alone would flag timestamps, ULIDs, and
// version strings).
var ipRun = regexp.MustCompile(`[0-9A-Fa-f:.]{2,}(?:%[0-9A-Za-z]{1,15})?`)

// maxIPLiteralLen bounds the length of the sub-candidates considered inside
// a matched run. The longest legal IP literal this scanner needs to
// recognise — an IPv6 address with an embedded IPv4 tail plus an RFC 4007
// zone identifier, e.g. "ffff:ffff:ffff:ffff:ffff:ffff:255.255.255.255%wlan0"
// — is well under this bound. Capping candidate length keeps sub-candidate
// generation O(n) instead of O(n^2) for a pathological run (e.g. a blob
// consisting of thousands of ':' characters) without ever truncating a real
// IP literal, since no valid literal is anywhere near this long.
const maxIPLiteralLen = 64

// unspecifiedIPv6Literal is the bare, two-character "::" token: the textual
// form of the IPv6 unspecified address (all-zero, the v6 analogue of
// "0.0.0.0"). It is deliberately excluded from detection: as a two-byte
// token consisting solely of punctuation that is *also* the scope-resolution
// operator in C++/Rust/Ruby and a common separator in Markdown/YAML, it is
// never how a real client identifier appears in telemetry — flagging it
// would make the gate fire constantly on ordinary text and get disabled,
// which is the exact failure mode this scanner exists to avoid (see the
// package doc). Every OTHER compressed form (e.g. "::1", "2001:db8::",
// "fe80::1", "::ffff:203.0.113.5") is still fully detected.
const unspecifiedIPv6Literal = "::"

// FindIPLiterals scans data for byte sequences that parse as a valid IPv4 or
// IPv6 address — including IPv6 "::" compression, IPv4-mapped IPv6 addresses
// such as "::ffff:203.0.113.5" (the form Go's dual-stack net.Conn hands back
// for the client's remote address), and IPv6 zone identifiers such as
// "fe80::1%eth0" — and returns every distinct match found (nil if none).
// Callers should pass the FULL serialized artifact — a marshalled protobuf
// event, a JSON log line, an entire WAL record on disk — rather than
// pre-selecting individual fields to check.
//
// The scanner is DEFAULT-DENY and never trims characters off a candidate
// based on an assumption about what is "probably surrounding punctuation".
// Blind trimming (e.g. stripping leading/trailing ':' and '.') silently
// destroys the very syntax that makes compressed/mapped IPv6 literals valid:
// "::1" would become "1", "2001:db8::" would become "2001:db8", and
// "::ffff:203.0.113.5" would become "ffff:203.0.113.5" — all three fail to
// parse, letting a real client IP through undetected. Instead, for every
// maximal run of IP-literal characters, FindIPLiterals tries contiguous
// sub-runs (bounded by maxIPLiteralLen) — varying BOTH where the candidate
// starts and where it ends within the run, preferring the longest match at
// the earliest starting offset — and keeps the first one netip.ParseAddr
// accepts. This correctly recovers, for example, the IPv4 literal inside
// "X-Forwarded-For:203.0.113.5" (a leading ':' that is not itself part of
// any valid IP) without ever discarding a leading/trailing ':' that IS
// semantically part of a compressed IPv6 address.
//
// Output order is the order candidates are encountered scanning left to
// right through data; the same input always yields the same output.
func FindIPLiterals(data []byte) []string {
	var found []string
	seen := make(map[string]struct{})
	s := string(data)
	for _, loc := range ipRun.FindAllStringIndex(s, -1) {
		run := s[loc[0]:loc[1]]
		for i := 0; i < len(run); {
			maxLen := len(run) - i
			if maxLen > maxIPLiteralLen {
				maxLen = maxIPLiteralLen
			}
			matchLen := 0
			for length := maxLen; length >= 2; length-- {
				cand := run[i : i+length]
				if cand == unspecifiedIPv6Literal {
					continue
				}
				if _, err := netip.ParseAddr(cand); err != nil {
					continue
				}
				if _, dup := seen[cand]; !dup {
					seen[cand] = struct{}{}
					found = append(found, cand)
				}
				matchLen = length
				break
			}
			if matchLen == 0 {
				i++
				continue
			}
			i += matchLen
		}
	}
	return found
}

// ContainsIPLiteral reports whether data contains any substring that parses
// as a valid IPv4 or IPv6 address. Convenience wrapper around FindIPLiterals
// for callers that only need a boolean (e.g. t.Fatal on the first hit).
func ContainsIPLiteral(data []byte) bool {
	return len(FindIPLiterals(data)) > 0
}
