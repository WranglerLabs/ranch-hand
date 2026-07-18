//go:build windows

package adapter

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func WSLDistributions(ctx context.Context) ([]string, error) {
	command := exec.CommandContext(ctx, "wsl.exe", "--list", "--quiet")
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("WSL2 is unavailable or no distribution is installed")
	}
	decoded := decodeWSLText(output)
	var result []string
	for _, line := range strings.Split(decoded, "\n") {
		name := strings.TrimSpace(line)
		if name != "" && !strings.HasPrefix(strings.ToLower(name), "docker-desktop") {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no ordinary WSL distribution is installed")
	}
	return result, nil
}

func decodeWSLText(value []byte) string {
	if !bytes.Contains(value, []byte{0}) {
		return string(value)
	}
	if len(value)%2 != 0 {
		value = value[:len(value)-1]
	}
	words := make([]uint16, len(value)/2)
	for index := range words {
		words[index] = binary.LittleEndian.Uint16(value[index*2:])
	}
	return string(utf16.Decode(words))
}

func connectWSL(_ context.Context, candidate plan.DeploymentPlan, _ Credentials) (remoteHost, error) {
	distribution := candidate.Configuration["distribution"]
	if distribution == "" {
		distribution = candidate.Configuration["host"]
	}
	if distribution == "" || strings.ContainsAny(distribution, "\r\n\x00") {
		return nil, errors.New("a valid WSL distribution is required")
	}
	return &wslHost{distribution: distribution, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

type wslHost struct {
	distribution string
	client       *http.Client
}

func (h *wslHost) Run(ctx context.Context, command string, stdin []byte) (string, error) {
	script := remoteShellScript(command, stdin)
	process := exec.CommandContext(ctx, "wsl.exe", "-d", h.distribution, "--", "sh", "-s")
	process.Stdin = strings.NewReader(script)
	output := &limitedOutput{maximum: 64 << 10}
	process.Stdout = output
	process.Stderr = output
	err := process.Run()
	if output.truncated {
		return "", errors.New("WSL command output exceeded 64 KiB")
	}
	return strings.TrimSpace(output.String()), err
}

func (h *wslHost) Health(ctx context.Context, requestPath string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080"+requestPath, nil)
	if err != nil {
		return 0, nil, err
	}
	response, err := h.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPlaneResponse+1))
	if err != nil || len(body) > maxControlPlaneResponse {
		return response.StatusCode, nil, errors.New("WSL health response exceeded the safety limit")
	}
	return response.StatusCode, body, nil
}

func (h *wslHost) Close() error { return nil }

func loadWSLImageArchive(ctx context.Context, distribution, archive, runtimeImage string) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("open verified WSL image archive: %w", err)
	}
	defer file.Close()
	command := exec.CommandContext(ctx, "wsl.exe", "-d", distribution, "--", "docker", "image", "load")
	command.Stdin = file
	output := &limitedOutput{maximum: 64 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return fmt.Errorf("load verified release image into WSL Docker Engine: %w: %s", err, boundedCommandFailure(output.String()))
	}
	host := &wslHost{distribution: distribution, client: &http.Client{Timeout: 30 * time.Second}}
	loaded, err := host.Run(ctx, "docker image inspect --format '{{.Id}}' "+shellQuote(runtimeImage), nil)
	if err != nil || loaded != "sha256:89d1b4091137eef57c91270d363fb6c76e6d60c94dcac92b129b2b8629f45093" {
		return errors.New("loaded WSL image does not match the verified RepoWrangler release identity")
	}
	return nil
}
