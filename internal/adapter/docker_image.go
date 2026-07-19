package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// prepareLocalCompanion downloads the independently published, digest-verified
// release archive, loads it through Docker Desktop's native Engine API, and
// confirms that the loaded image has an ID in the immutable trust record. This
// deliberately avoids registry authentication and never shells out to Docker.
func (d *LocalDocker) prepareLocalCompanion(ctx context.Context, image string) (string, error) {
	if d.prepareImage != nil {
		return d.prepareImage(ctx, image)
	}
	companion, err := companionForImage(image)
	if err != nil {
		return "", err
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate Ranch Hand image cache: %w", err)
	}
	archive, err := cacheCompanionImage(ctx, companion, &http.Client{Timeout: 30 * time.Minute}, filepath.Join(root, "WranglerLabs", "Ranch Hand"))
	if err != nil {
		return "", err
	}
	file, err := os.Open(archive)
	if err != nil {
		return "", fmt.Errorf("open verified image archive: %w", err)
	}
	defer file.Close()
	if err := d.loadImageArchive(ctx, file); err != nil {
		return "", err
	}
	var inspected struct {
		ID string `json:"Id"`
	}
	if err := d.dockerJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(companion.runtimeImage)+"/json", nil, nil, http.StatusOK, &inspected); err != nil {
		return "", fmt.Errorf("inspect loaded release image: %w", err)
	}
	if !companionLoadedImageMatches(companion, inspected.ID) {
		return "", fmt.Errorf("loaded release image identity %q is not in the verified immutable image trust record", strings.TrimSpace(inspected.ID))
	}
	return companion.runtimeImage, nil
}

func (d *LocalDocker) loadImageArchive(ctx context.Context, archive io.Reader) error {
	baseURL := d.baseURL
	if baseURL == "" {
		baseURL = "http://docker"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/images/load?quiet=1", archive)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-tar")
	response, err := d.client.Do(request)
	if err != nil {
		return fmt.Errorf("load verified release image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("load verified release image: Docker Engine returned HTTP %d", response.StatusCode)
	}
	limited := &io.LimitedReader{R: response.Body, N: maximumDockerResponse + 1}
	decoder := json.NewDecoder(limited)
	for events := 0; ; events++ {
		if events > 100_000 {
			return errors.New("Docker Engine image-load stream exceeded the event safety limit")
		}
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); errors.Is(err, io.EOF) {
			if limited.N == 0 {
				return errors.New("Docker Engine image-load stream exceeded the response safety limit")
			}
			return nil
		} else if err != nil {
			return errors.New("Docker Engine returned an invalid image-load stream")
		}
		if message.Error != "" || message.ErrorDetail.Message != "" {
			return errors.New("Docker Engine could not load the verified release image")
		}
	}
}
