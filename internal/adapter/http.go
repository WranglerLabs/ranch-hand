package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const maxControlPlaneResponse = 1 << 20

func controlPlaneJSON(ctx context.Context, client *http.Client, method, destination string, headers map[string]string, output any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, destination, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPlaneResponse+1))
	if err != nil {
		return response.StatusCode, err
	}
	if len(body) > maxControlPlaneResponse {
		return response.StatusCode, errors.New("control-plane response exceeded the 1 MiB safety limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	if output != nil && len(body) > 0 {
		if err := json.Unmarshal(body, output); err != nil {
			return response.StatusCode, fmt.Errorf("decode control-plane response: %w", err)
		}
	}
	return response.StatusCode, nil
}
