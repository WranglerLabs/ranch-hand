//go:build !windows

package adapter

import (
	"context"
	"errors"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func WSLDistributions(context.Context) ([]string, error) {
	return nil, errors.New("WSL deployment is available only in Ranch Hand for Windows")
}

func connectWSL(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) {
	return nil, errors.New("WSL deployment is available only in Ranch Hand for Windows")
}

func wslPersistenceConfigured() (bool, error) {
	return false, errors.New("WSL deployment is available only in Ranch Hand for Windows")
}

func ensureWSLPersistence(context.Context) error {
	return errors.New("WSL persistence configuration is available only in the Windows build")
}

func installWSLDockerPrerequisites(context.Context, string, string) error {
	return errors.New("guided WSL Docker installation is available only in the Windows build")
}

func loadWSLImageArchive(context.Context, string, string, companionImage) error {
	return errors.New("WSL deployment is available only in Ranch Hand for Windows")
}
