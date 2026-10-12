package webpush

import "strings"

// DeviceLabel names a subscribing browser from its user agent, such as
// "Chrome on Android", so the owner can tell their devices apart. Chrome's
// reduced user agent still names the browser and the platform.
func DeviceLabel(userAgent string) string {
	browser, platform := browserName(userAgent), platformName(userAgent)
	switch {
	case browser != "" && platform != "":
		return browser + " on " + platform
	case browser != "":
		return browser
	case platform != "":
		return "Browser on " + platform
	default:
		return "Unknown browser"
	}
}

func browserName(ua string) string {
	switch {
	case containsAny(ua, "EdgA/", "EdgiOS/", "Edg/"):
		return "Edge"
	case strings.Contains(ua, "SamsungBrowser/"):
		return "Samsung Internet"
	case containsAny(ua, "OPR/", "OPT/", "Opera"):
		return "Opera"
	case containsAny(ua, "Firefox/", "FxiOS/"):
		return "Firefox"
	case containsAny(ua, "CriOS/", "Chrome/", "Chromium/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		return "Safari"
	}
	return ""
}

func platformName(ua string) string {
	switch {
	case strings.Contains(ua, "Android"):
		return "Android"
	case containsAny(ua, "iPhone", "iPad", "iPod"):
		return "iOS"
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case containsAny(ua, "Macintosh", "Mac OS X"):
		return "macOS"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	}
	return ""
}

func containsAny(s string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}
