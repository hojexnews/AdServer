// Package useragent reduces a raw User-Agent string to a coarse
// device/browser class string suitable for telemetry.
//
// # Privacy (TX-5/DA-11)
//
// The raw User-Agent is a fingerprinting vector.  This package is the ONLY
// location where the raw UA string is processed.  It returns a coarse class
// such as "chrome-desktop", "safari-mobile", or "bot".
//
// The raw UA MUST NOT be emitted, persisted, logged, or forwarded anywhere
// in the system.  Callers receive only the class string returned by Classify.
//
// # Classes (non-exhaustive; unknown always falls back to "other")
//
//	bot             — bots, crawlers, scrapers
//	chrome-mobile   — Chrome on Android/iOS
//	chrome-desktop  — Chrome on Windows/Mac/Linux
//	safari-mobile   — Safari on iPhone/iPad
//	safari-desktop  — Safari on macOS
//	firefox-mobile  — Firefox on Android
//	firefox-desktop — Firefox on desktop
//	edge-desktop    — Microsoft Edge
//	other           — anything not matched above
package useragent

import "strings"

// Classify returns a coarse device/browser class for the given raw
// User-Agent string.  The raw UA is consumed here and NEVER returned.
//
// The returned string is one of the documented classes and is safe to
// persist in telemetry as it carries no PII.
func Classify(rawUA string) string {
	ua := strings.ToLower(rawUA)

	// Detect bots first — highest priority.
	if isBot(ua) {
		return "bot"
	}

	// Mobile detection: must come before desktop Chrome/Firefox because
	// mobile browsers often include "safari" or "firefox" in their UA too.
	mobile := strings.Contains(ua, "mobile") ||
		strings.Contains(ua, "android") ||
		strings.Contains(ua, "iphone") ||
		strings.Contains(ua, "ipad")

	// Edge (Chromium-based Edge identifies as "edg/").
	if strings.Contains(ua, "edg/") || strings.Contains(ua, "edge/") {
		return "edge-desktop"
	}

	// Chrome (must check after Edge because Edge also contains "chrome").
	if strings.Contains(ua, "chrome/") || strings.Contains(ua, "chromium/") {
		if mobile {
			return "chrome-mobile"
		}
		return "chrome-desktop"
	}

	// Firefox.
	if strings.Contains(ua, "firefox/") {
		if mobile {
			return "firefox-mobile"
		}
		return "firefox-desktop"
	}

	// Safari (must check after Chrome/Edge since those also contain "safari").
	if strings.Contains(ua, "safari/") {
		if mobile {
			return "safari-mobile"
		}
		return "safari-desktop"
	}

	return "other"
}

// isBot returns true when the UA string indicates a known bot or crawler.
func isBot(ua string) bool {
	botSignals := []string{
		"bot", "crawl", "spider", "slurp", "facebookexternalhit",
		"twitterbot", "linkedinbot", "googlebot", "bingbot", "yandexbot",
		"baiduspider", "duckduckbot", "applebot", "pingdom", "uptimerobot",
		"curl/", "wget/", "python-requests", "go-http-client", "java/",
		"headlesschrome", "phantomjs", "selenium",
	}
	for _, sig := range botSignals {
		if strings.Contains(ua, sig) {
			return true
		}
	}
	return false
}
