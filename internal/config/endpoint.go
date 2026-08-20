package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ParseOTLPEndpoint validates the v0 loopback-only OTLP/HTTP metrics URL.
// Errors intentionally omit the supplied URL because it may contain secrets.
func ParseOTLPEndpoint(value string) (*url.URL, error) {
	endpoint, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("OTLP endpoint is not a valid URL")
	}
	if endpoint.Scheme != "http" || endpoint.Opaque != "" {
		return nil, fmt.Errorf("OTLP endpoint must use HTTP")
	}
	if endpoint.User != nil {
		return nil, fmt.Errorf("OTLP endpoint must not contain user information")
	}
	if endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" {
		return nil, fmt.Errorf("OTLP endpoint must not contain a query or fragment")
	}
	if endpoint.Host == "" || endpoint.Hostname() == "" {
		return nil, fmt.Errorf("OTLP endpoint must contain a host")
	}
	if !isLoopbackHost(endpoint.Hostname()) {
		return nil, fmt.Errorf("OTLP endpoint host must be loopback")
	}
	if !strings.HasSuffix(endpoint.Path, "/v1/metrics") {
		return nil, fmt.Errorf("OTLP endpoint path must end in /v1/metrics")
	}
	if endpoint.RawPath != "" && !strings.HasSuffix(endpoint.EscapedPath(), "/v1/metrics") {
		return nil, fmt.Errorf("OTLP endpoint path must end in /v1/metrics")
	}
	return endpoint, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
