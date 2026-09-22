package delivery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"time"
)

func ValidateURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid destination URL")
	}
	if u.Scheme != "https" && !(allowPrivate && u.Scheme == "http") {
		return fmt.Errorf("destination must use HTTPS")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !allowPrivate && !publicIP(ip) {
		return fmt.Errorf("destination address is not public")
	}
	return nil
}

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blocked {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Dial the IP we just checked. Looking up the name again later would allow a rebind.
func safeDial(allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("destination DNS lookup failed")
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("destination has no addresses")
		}
		for _, ip := range ips {
			if !allowPrivate && !publicIP(ip) {
				return nil, fmt.Errorf("destination resolved to a forbidden address")
			}
		}
		dialer := net.Dialer{Timeout: 5 * time.Second}
		for _, ip := range ips {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			err = e
		}
		return nil, fmt.Errorf("destination connection failed")
	}
}
