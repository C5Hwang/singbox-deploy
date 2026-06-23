package ui

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var publicIPEndpoints = []string{
	"https://api.ipify.org",
	"https://api64.ipify.org",
	"https://icanhazip.com",
}

func validateDomainResolvesToCurrentIP(ctx context.Context, domain string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("domain is required")
	}

	domainIPs, err := net.DefaultResolver.LookupIP(ctx, "ip", domain)
	if err != nil {
		return fmt.Errorf("resolve domain: %w", err)
	}
	if len(domainIPs) == 0 {
		return fmt.Errorf("domain does not resolve to any IP")
	}

	currentIPs, err := currentPublicIPs(ctx)
	if err != nil {
		return err
	}
	if anyIPMatches(domainIPs, currentIPs) {
		return nil
	}
	return fmt.Errorf("domain resolves to %s, current public IP is %s", formatIPs(domainIPs), formatIPs(currentIPs))
}

// validateDomainResolvesToIP confirms domain resolves to expected, which may be
// an IPv4/IPv6 literal or a hostname (resolved first). Used by add-node where
// the operator entered the node's public IP/host explicitly, so we don't need
// to query the public IP service.
func validateDomainResolvesToIP(ctx context.Context, domain, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return fmt.Errorf("node public IP must be entered before validating the domain")
	}

	domainIPs, err := net.DefaultResolver.LookupIP(ctx, "ip", domain)
	if err != nil {
		return fmt.Errorf("resolve domain: %w", err)
	}
	if len(domainIPs) == 0 {
		return fmt.Errorf("domain does not resolve to any IP")
	}

	var expectedIPs []net.IP
	if ip := net.ParseIP(expected); ip != nil {
		expectedIPs = []net.IP{ip}
	} else {
		expectedIPs, err = net.DefaultResolver.LookupIP(ctx, "ip", expected)
		if err != nil {
			return fmt.Errorf("resolve node host %q: %w", expected, err)
		}
		if len(expectedIPs) == 0 {
			return fmt.Errorf("node host %q does not resolve to any IP", expected)
		}
	}

	if anyIPMatches(domainIPs, expectedIPs) {
		return nil
	}
	return fmt.Errorf("domain resolves to %s, node IP is %s", formatIPs(domainIPs), formatIPs(expectedIPs))
}

func currentPublicIPs(ctx context.Context) ([]net.IP, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	var ips []net.IP
	var failures []string

	for _, endpoint := range publicIPEndpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			failures = append(failures, endpoint+": "+err.Error())
			continue
		}
		req.Header.Set("User-Agent", "singbox-deploy")

		resp, err := client.Do(req)
		if err != nil {
			failures = append(failures, endpoint+": "+err.Error())
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128))
		closeErr := resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			failures = append(failures, fmt.Sprintf("%s: HTTP %d", endpoint, resp.StatusCode))
			continue
		}
		if readErr != nil {
			failures = append(failures, endpoint+": "+readErr.Error())
			continue
		}
		if closeErr != nil {
			failures = append(failures, endpoint+": "+closeErr.Error())
			continue
		}

		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip == nil {
			failures = append(failures, endpoint+": invalid IP response")
			continue
		}
		if !containsIP(ips, ip) {
			ips = append(ips, ip)
		}
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("detect current public IP: %s", strings.Join(failures, "; "))
	}
	return ips, nil
}

func anyIPMatches(left, right []net.IP) bool {
	for _, a := range left {
		if containsIP(right, a) {
			return true
		}
	}
	return false
}

func containsIP(ips []net.IP, ip net.IP) bool {
	for _, existing := range ips {
		if existing.Equal(ip) {
			return true
		}
	}
	return false
}

func formatIPs(ips []net.IP) string {
	if len(ips) == 0 {
		return "none"
	}
	vals := make([]string, 0, len(ips))
	for _, ip := range ips {
		vals = append(vals, ip.String())
	}
	return strings.Join(vals, ", ")
}
