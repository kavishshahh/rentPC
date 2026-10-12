package hwdetect

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"time"
)

// TLSInterception probes a well-known HTTPS endpoint and reports whether the
// certificate chain the host sees is issued by someone other than the
// expected public CA. Corporate proxies (Netskope, Zscaler, Fortinet…) and
// some antivirus products re-sign TLS; the host trusts them because their
// root was installed on the host, but a sandbox VM has a clean trust store
// and will (correctly) refuse every HTTPS connection. We refuse to make the
// VM trust the interceptor — that would expose renters' traffic — so the
// host needs to know.
//
// Returns ("", false) when the chain looks genuine, (issuer, true) when it
// looks intercepted, and ("", false) with err set when the probe failed.
func TLSInterception(ctx context.Context) (issuer string, intercepted bool, err error) {
	d := &net.Dialer{Timeout: 8 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", "www.google.com:443")
	if err != nil {
		return "", false, err
	}
	defer raw.Close()
	c := tls.Client(raw, &tls.Config{ServerName: "www.google.com", InsecureSkipVerify: true}) //nolint:gosec // inspection only
	if err := c.HandshakeContext(ctx); err != nil {
		return "", false, err
	}
	certs := c.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", false, nil
	}
	iss := strings.Join(certs[0].Issuer.Organization, " ")
	if iss == "" {
		iss = certs[0].Issuer.CommonName
	}
	if strings.Contains(strings.ToLower(iss), "google trust services") {
		return "", false, nil
	}
	return iss, true, nil
}
