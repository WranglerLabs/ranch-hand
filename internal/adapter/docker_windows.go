//go:build windows

package adapter

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"

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

func installDockerDesktop(ctx context.Context) error {
	winget, err := exec.LookPath("winget.exe")
	if err != nil {
		return errors.New("Windows Package Manager (winget) is required for guided Docker Desktop installation")
	}
	command := exec.CommandContext(ctx, winget, "install", "--id", "Docker.DockerDesktop", "--exact", "--source", "winget", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
	output := &limitedOutput{maximum: 64 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("install Docker Desktop with winget: %s", strings.TrimSpace(output.String()))
	}
	return nil
}
