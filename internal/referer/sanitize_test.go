package referer

import "testing"

func TestSanitize_StripsQueryAndFragment(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://example.com/page?session=abc123&user=456", "https://example.com/page"},
		{"https://example.com/page#section", "https://example.com/page"},
		{"https://example.com/page?q=1#frag", "https://example.com/page"},
		{"https://example.com/path/to/page", "https://example.com/path/to/page"},
		{"", ""},
		{"not-a-url", ""},                // parse error / no scheme → empty string
		{"javascript:alert(1)", ""},      // non-http(s) scheme rejected
		{"http:///no-host?x=1", ""},      // absolute scheme but no host
	}
	for _, tc := range cases {
		got := Sanitize(tc.raw)
		if got != tc.want {
			t.Errorf("Sanitize(%q): got %q, want %q", tc.raw, got, tc.want)
		}
	}
}
