package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// ValidateRecognizerURL permits cleartext only on loopback or an explicitly
// trusted private transport configured by the operator.
func ValidateRecognizerURL(value string, allowInsecure bool) error {
	u, e := url.Parse(value)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("invalid RECOGNIZER_URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return fmt.Errorf("RECOGNIZER_URL requires HTTPS")
	}
	address, e := netip.ParseAddr(u.Hostname())
	if allowInsecure || strings.EqualFold(u.Hostname(), "localhost") || e == nil && address.IsLoopback() {
		return nil
	}
	return fmt.Errorf("remote HTTP requires explicit ALLOW_INSECURE_RECOGNIZER for a trusted private transport")
}
