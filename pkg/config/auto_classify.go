package config

import (
	"strings"
)

// AutoClassifyByDomain automatically classifies contributors by email domain
// when no vendor config is provided
func AutoClassifyByDomain(email string) string {
	if email == "" {
		return "unknown"
	}

	// Extract domain from email
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return "invalid-email"
	}

	domain := strings.ToLower(parts[1])

	// Group common personal email providers as "community"
	personalDomains := map[string]bool{
		"gmail.com":       true,
		"yahoo.com":       true,
		"hotmail.com":     true,
		"outlook.com":     true,
		"protonmail.com":  true,
		"icloud.com":      true,
		"mail.com":        true,
		"aol.com":         true,
		"yandex.com":      true,
		"qq.com":          true,
		"163.com":         true,
		"126.com":         true,
		"sina.com":        true,
		"live.com":        true,
		"msn.com":         true,
		"me.com":          true,
		"mac.com":         true,
		"googlemail.com":  true,
		"yahoo.co.uk":     true,
		"yahoo.co.jp":     true,
		"fastmail.com":    true,
		"zoho.com":        true,
	}

	if personalDomains[domain] {
		return "community"
	}

	// Return the domain as the vendor name
	return "@" + domain
}
