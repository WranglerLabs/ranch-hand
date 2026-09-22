//go:build windows

package adapter

import (
	"context"
	"net"
	"net/http"

	"github.com/Microsoft/go-winio"
)

func localDockerTransport() http.RoundTripper {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, `\\.\pipe\docker_engine`)
		},
		DisableCompression: true,
	}
}
