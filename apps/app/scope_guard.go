package app

import (
	"fmt"
	"strings"
)

func enforceOutboundURL(rawURL string) error {
	return scopeManager.EnforceSend(rawURL)
}

func outboundURLFromParts(tls bool, host, port, rawRequest string) string {
	scheme := "http"
	defaultPort := "80"
	if tls {
		scheme = "https"
		defaultPort = "443"
	}
	if port == "" {
		port = defaultPort
	}
	path := "/"
	if fields := strings.Fields(rawRequest); len(fields) >= 2 {
		path = fields[1]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/"
	}
	if port == defaultPort {
		return fmt.Sprintf("%s://%s%s", scheme, host, path)
	}
	return fmt.Sprintf("%s://%s:%s%s", scheme, host, port, path)
}
