//go:build !windows

package adapter

import (
	"context"
	"errors"
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

func installDockerDesktop(context.Context) error {
	return errors.New("guided Docker Desktop installation is available only in the Windows build")
}
