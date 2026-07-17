//go:build !windows

package adapter

import (
	"context"
	"net"
	"net/http"
)

func localDockerTransport() http.RoundTripper {
	dialer := &net.Dialer{}
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", "/var/run/docker.sock")
		},
		DisableCompression: true,
	}
}
