package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

// prepareRemoteCompanion downloads the release's independently published,
// digest-verified image archive to the control workstation, streams it over
// the pinned SSH connection, and confirms Docker loaded the expected immutable
// image ID. The target never needs registry credentials or direct registry
// access.
func (a *RemoteLinuxCompose) prepareRemoteCompanion(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials, image string) (string, error) {
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
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		return "", err
	}
	defer host.Close()
	loader, ok := host.(remoteImageLoader)
	if !ok {
		return "", errors.New("the remote SSH transport cannot stream a verified image archive")
	}
	file, err := os.Open(archive)
	if err != nil {
		return "", fmt.Errorf("open verified image archive: %w", err)
	}
	defer file.Close()
	output, err := loader.LoadImage(ctx, file)
	if err != nil {
		if output != "" {
			return "", fmt.Errorf("load verified release image over SSH: %w: %s", err, boundedCommandFailure(output))
		}
		return "", fmt.Errorf("load verified release image over SSH: %w", err)
	}
	loadedID, inspectErr := host.Run(ctx, "docker image inspect --format '{{.Id}}' "+shellQuote(companion.runtimeImage), nil)
	if inspectErr != nil {
		return "", fmt.Errorf("inspect loaded release image: %w", inspectErr)
	}
	if strings.TrimSpace(loadedID) != companion.imageID {
		return "", errors.New("loaded release image ID does not match the verified immutable image")
	}
	return companion.runtimeImage, nil
}
