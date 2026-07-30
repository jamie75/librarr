package diagnostics

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type RTorrentConfig struct {
	URL      string
	Username string
	Password string
	Timeout  time.Duration
	Test     func(context.Context) (string, error)
}

// DiagnoseRTorrent keeps the same staged shape as the existing integration
// diagnostics. The adapter owns the XML-RPC call; this layer explains the
// operator-facing configuration and protocol stages without exposing secrets.
func DiagnoseRTorrent(ctx context.Context, cfg RTorrentConfig) Result {
	r := newRunner("rtorrent")
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if step := timedStep("Configuration", func() (string, string, error) {
		if strings.TrimSpace(cfg.URL) == "" {
			return "rTorrent RPC URL is missing.", "Set the XML-RPC endpoint in Settings.", fmt.Errorf("missing rTorrent URL")
		}
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Scheme == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "rTorrent RPC URL is invalid.", "Use a full http:// or https:// XML-RPC URL.", fmt.Errorf("invalid rTorrent URL")
		}
		if u.User != nil {
			return "rTorrent RPC URL must not contain credentials.", "Use the separate username and password fields.", fmt.Errorf("credentials in URL")
		}
		return "RPC URL is configured.", "", nil
	}); step.Status == StatusFailed {
		r.add(step)
		return r.finish()
	} else {
		r.add(step)
	}
	var ep *endpoint
	if step := timedStep("URL Valid", func() (string, string, error) {
		var prepareErr error
		ep, prepareErr = prepareEndpoint(cfg.URL)
		if prepareErr != nil {
			return "rTorrent RPC URL is invalid.", "Use a full http:// or https:// XML-RPC URL.", prepareErr
		}
		return ep.Origin, "", nil
	}); step.Status == StatusFailed {
		r.add(step)
		return r.finish()
	} else {
		r.add(step)
	}
	if step := timedStep("DNS Lookup", func() (string, string, error) {
		addrs, lookupErr := lookupDNS(ctx, ep.Host)
		if lookupErr != nil {
			return "Unable to resolve rTorrent hostname.", "Verify seedbox DNS and Docker network access.", lookupErr
		}
		return fmt.Sprintf("Resolved %d address(es).", len(addrs)), "", nil
	}); step.Status == StatusFailed {
		r.add(step)
		return r.finish()
	} else {
		r.add(step)
	}
	if step := timedStep("TCP Connection", func() (string, string, error) {
		if connectErr := tcpConnect(ctx, ep.Host, ep.Port, cfg.Timeout); connectErr != nil {
			return "Could not connect to the rTorrent RPC port.", "Verify the endpoint, firewall, and reverse proxy.", connectErr
		}
		return "TCP connection succeeded.", "", nil
	}); step.Status == StatusFailed {
		r.add(step)
		return r.finish()
	} else {
		r.add(step)
	}
	if ep.URL.Scheme == "https" {
		if step := timedStep("TLS", func() (string, string, error) {
			if tlsErr := tlsHandshake(ctx, ep.Host, ep.Port, cfg.Timeout); tlsErr != nil {
				return "TLS verification failed.", "Check the seedbox certificate or use HTTP only when appropriate.", tlsErr
			}
			return "TLS handshake succeeded.", "", nil
		}); step.Status == StatusFailed {
			r.add(step)
			return r.finish()
		} else {
			r.add(step)
		}
	} else {
		r.add(Step{Name: "TLS", Status: StatusSkipped, Message: "HTTP endpoint; TLS was not used."})
	}

	if step := timedStep("XML-RPC", func() (string, string, error) {
		if cfg.Test == nil {
			return "No XML-RPC test is configured.", "Retry after restarting Librarr.", fmt.Errorf("missing XML-RPC test")
		}
		testCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
		version, err := cfg.Test(testCtx)
		if err != nil {
			return classifyRTorrentError(err), "Verify the RPC endpoint, credentials, reverse proxy, and rTorrent XML-RPC configuration.", err
		}
		if version == "" {
			return "XML-RPC responded without a version.", "Verify the rTorrent system.client_version method.", nil
		}
		return "XML-RPC responded; rTorrent " + version, "", nil
	}); step.Status == StatusFailed {
		r.add(step)
		return r.finish()
	} else {
		r.add(step)
	}
	r.add(Step{Name: "Authentication", Status: StatusSuccess, Message: "Credentials accepted or endpoint does not require authentication."})
	return r.finish()
}

func classifyRTorrentError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "authentication"):
		return "Authentication rejected by rTorrent."
	case strings.Contains(message, "invalid xml"):
		return "rTorrent endpoint returned invalid XML-RPC."
	case strings.Contains(message, "html"):
		return "rTorrent endpoint returned HTML instead of XML-RPC."
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "rTorrent XML-RPC request timed out."
	default:
		return "rTorrent XML-RPC request failed."
	}
}
