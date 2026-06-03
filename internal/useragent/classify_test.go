package useragent_test

import (
	"testing"

	"github.com/hojex/adserver/internal/useragent"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		rawUA    string
		wantClass string
	}{
		{
			name:      "Chrome desktop",
			rawUA:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
			wantClass: "chrome-desktop",
		},
		{
			name:      "Chrome mobile Android",
			rawUA:     "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
			wantClass: "chrome-mobile",
		},
		{
			name:      "Safari desktop macOS",
			rawUA:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_4) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			wantClass: "safari-desktop",
		},
		{
			name:      "Safari mobile iPhone",
			rawUA:     "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			wantClass: "safari-mobile",
		},
		{
			name:      "Firefox desktop",
			rawUA:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:124.0) Gecko/20100101 Firefox/124.0",
			wantClass: "firefox-desktop",
		},
		{
			name:      "Firefox mobile Android",
			rawUA:     "Mozilla/5.0 (Android 13; Mobile; rv:124.0) Gecko/124.0 Firefox/124.0",
			wantClass: "firefox-mobile",
		},
		{
			name:      "Edge desktop",
			rawUA:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0",
			wantClass: "edge-desktop",
		},
		{
			name:      "Googlebot",
			rawUA:     "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			wantClass: "bot",
		},
		{
			name:      "curl",
			rawUA:     "curl/8.4.0",
			wantClass: "bot",
		},
		{
			name:      "empty UA",
			rawUA:     "",
			wantClass: "other",
		},
		{
			name:      "unknown UA",
			rawUA:     "SomeUnknownBrowser/1.0",
			wantClass: "other",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := useragent.Classify(tc.rawUA)
			if got != tc.wantClass {
				t.Errorf("Classify(%q): got %q, want %q", tc.rawUA, got, tc.wantClass)
			}
		})
	}
}

// Verify raw UA never appears in the returned class (privacy — TX-5).
func TestClassify_RawUANotInClass(t *testing.T) {
	rawUAs := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/124.0.0.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1",
		"curl/8.4.0",
	}
	for _, ua := range rawUAs {
		class := useragent.Classify(ua)
		// The class must be a short opaque label, not the raw UA string.
		if class == ua {
			t.Errorf("Classify returned the raw UA string — PII leak: %q", ua)
		}
		// The class must be non-empty.
		if class == "" {
			t.Errorf("Classify returned empty string for UA %q", ua)
		}
		// The class must not contain the raw UA as a substring.
		if len(class) > 30 {
			t.Errorf("Classify returned suspiciously long class %q for UA %q", class, ua)
		}
	}
}
