package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const maximumDockerResponse = 64 << 20

var dockerProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func (d *LocalDocker) Backup(context.Context, plan.DeploymentPlan, Credentials) (lifecycle.BackupArtifact, error) {
	return lifecycle.BackupArtifact{}, errors.New("local Docker backup is not implemented")
}

func (d *LocalDocker) Apply(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, staged bundle.StagedBundle, _ Credentials) error {
	if kind != lifecycle.Install {
		return fmt.Errorf("local Docker %s is not implemented", kind)
	}
	project, dataVolume, hostIP, hostPort, err := localDockerInputs(candidate)
	if err != nil {
		return err
	}
	identity, err := bundle.ReadIdentity(staged)
	if err != nil {
		return err
	}
	if staged.Target != "local-compose" {
		return errors.New("local Docker adapter requires a local-compose bundle")
	}
	containerName := project + "-server"
	exists, _, _, err := d.containerMetadata(ctx, containerName)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("Docker container %q already exists; Ranch Hand will not replace an unmanaged or unjournaled container", containerName)
	}
	if err := d.pullImage(ctx, identity.Image); err != nil {
		return err
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	if err := d.ensureManagedVolume(ctx, dataVolume, deploymentID); err != nil {
		return err
	}
	payload := map[string]any{
		"Image":        identity.Image,
		"Env":          []string{"PORT=8080", "DEMO_MODE=true", "AUTH_PROVIDERS=github", "ENABLE_SCHEDULER=true", "SQLITE_PATH=/app/data/repo-wrangler.db", "APP_VERSION=" + candidate.Release.Version},
		"ExposedPorts": map[string]any{"8080/tcp": map[string]any{}},
		"Labels": map[string]string{
			"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID,
			"com.wranglerlabs.ranch-hand.version": candidate.Release.Version,
		},
		"HostConfig": map[string]any{
			"Mounts":        []map[string]string{{"Type": "volume", "Source": dataVolume, "Target": "/app/data"}},
			"PortBindings":  map[string]any{"8080/tcp": []map[string]string{{"HostIp": hostIP, "HostPort": hostPort}}},
			"RestartPolicy": map[string]any{"Name": "unless-stopped", "MaximumRetryCount": 0},
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	query := url.Values{"name": []string{containerName}}
	if err := d.dockerJSON(ctx, http.MethodPost, "/containers/create", query, payload, http.StatusCreated, &created); err != nil {
		return fmt.Errorf("create RepoWrangler container: %w", err)
	}
	if created.ID == "" {
		return errors.New("Docker Engine returned no created container identity")
	}
	if err := d.dockerJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(created.ID)+"/start", nil, nil, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("start RepoWrangler container: %w", err)
	}
	return nil
}

func (d *LocalDocker) Verify(ctx context.Context, candidate plan.DeploymentPlan, _ Credentials) error {
	_, _, _, hostPort, err := localDockerInputs(candidate)
	if err != nil {
		return err
	}
	client := d.healthClient
	if client == nil {
		client = loopbackHealthClient(hostPort)
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(deadline, http.MethodGet, "http://127.0.0.1/health/ready", nil)
		if err != nil {
			return err
		}
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return errors.New("RepoWrangler did not become ready within two minutes")
		case <-ticker.C:
		}
	}
}

func (d *LocalDocker) Recover(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, backup *lifecycle.BackupRecord, _ Credentials) error {
	if kind != lifecycle.Install || backup != nil {
		return errors.New("local Docker recovery currently supports only a partial install without a prior backup")
	}
	project := candidate.Configuration["projectName"]
	if !dockerProjectPattern.MatchString(project) {
		return errors.New("invalid local Docker project name")
	}
	containerName := project + "-server"
	exists, containerID, labels, err := d.containerMetadata(ctx, containerName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	if labels["com.wranglerlabs.ranch-hand.managed"] != "true" || labels["com.wranglerlabs.ranch-hand.deployment"] != deploymentID {
		return errors.New("refusing to remove a Docker container that is not owned by this Ranch Hand deployment")
	}
	return d.dockerJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(containerID), url.Values{"force": []string{"1"}, "v": []string{"1"}}, nil, http.StatusNoContent, nil)
}

var errDockerNotFound = errors.New("Docker resource not found")

func (d *LocalDocker) ensureManagedVolume(ctx context.Context, name, deploymentID string) error {
	var details struct {
		Labels map[string]string `json:"Labels"`
	}
	err := d.dockerJSON(ctx, http.MethodGet, "/volumes/"+url.PathEscape(name), nil, nil, http.StatusOK, &details)
	if err == nil {
		if details.Labels["com.wranglerlabs.ranch-hand.managed"] != "true" || details.Labels["com.wranglerlabs.ranch-hand.deployment"] != deploymentID {
			return errors.New("refusing to use a Docker volume that is not owned by this Ranch Hand deployment")
		}
		return nil
	}
	if !errors.Is(err, errDockerNotFound) {
		return err
	}
	payload := map[string]any{"Name": name, "Labels": map[string]string{
		"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID,
	}}
	if err := d.dockerJSON(ctx, http.MethodPost, "/volumes/create", nil, payload, http.StatusCreated, &details); err != nil {
		return fmt.Errorf("create managed RepoWrangler data volume: %w", err)
	}
	return nil
}

func (d *LocalDocker) containerMetadata(ctx context.Context, name string) (bool, string, map[string]string, error) {
	var details struct {
		ID     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	err := d.dockerJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, http.StatusOK, &details)
	if errors.Is(err, errDockerNotFound) {
		return false, "", nil, nil
	}
	if err != nil {
		return false, "", nil, err
	}
	if details.ID == "" {
		return false, "", nil, errors.New("Docker Engine returned a container without an identity")
	}
	return true, details.ID, details.Config.Labels, nil
}

func (d *LocalDocker) pullImage(ctx context.Context, image string) error {
	query := url.Values{"fromImage": []string{image}}
	response, err := d.dockerRequest(ctx, http.MethodPost, "/images/create", query, nil)
	if err != nil {
		return fmt.Errorf("pull immutable RepoWrangler image: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("pull immutable RepoWrangler image: Docker Engine returned HTTP %d", response.StatusCode)
	}
	limited := &io.LimitedReader{R: response.Body, N: maximumDockerResponse + 1}
	decoder := json.NewDecoder(limited)
	var consumed int64
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&message); errors.Is(err, io.EOF) {
			if limited.N == 0 {
				return errors.New("Docker Engine image-pull stream exceeded the response safety limit")
			}
			break
		} else if err != nil {
			return errors.New("Docker Engine returned an invalid image-pull stream")
		}
		consumed++
		if consumed > 100_000 {
			return errors.New("Docker Engine image-pull stream exceeded the event safety limit")
		}
		if message.Error != "" || message.ErrorDetail.Message != "" {
			return errors.New("Docker Engine could not pull the immutable RepoWrangler image")
		}
	}
	return nil
}

func (d *LocalDocker) dockerJSON(ctx context.Context, method, endpoint string, query url.Values, input any, expectedStatus int, output any) error {
	response, err := d.dockerRequest(ctx, method, endpoint, query, input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return errDockerNotFound
	}
	if response.StatusCode != expectedStatus {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("Docker Engine returned HTTP %d", response.StatusCode)
	}
	if output != nil {
		limited := &io.LimitedReader{R: response.Body, N: maximumDockerResponse + 1}
		decoder := json.NewDecoder(limited)
		if err := decoder.Decode(output); err != nil {
			return errors.New("Docker Engine returned invalid JSON")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return errors.New("Docker Engine returned invalid trailing JSON")
		}
		if limited.N == 0 {
			return errors.New("Docker Engine response exceeded the safety limit")
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	}
	return nil
}

func (d *LocalDocker) dockerRequest(ctx context.Context, method, endpoint string, query url.Values, input any) (*http.Response, error) {
	baseURL := d.baseURL
	if baseURL == "" {
		baseURL = "http://docker"
	}
	destination := baseURL + endpoint
	if len(query) > 0 {
		destination += "?" + query.Encode()
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, destination, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return d.client.Do(request)
}

func localDockerInputs(candidate plan.DeploymentPlan) (project, dataVolume, hostIP, hostPort string, err error) {
	if err = candidate.Validate(); err != nil {
		return
	}
	project = candidate.Configuration["projectName"]
	if !dockerProjectPattern.MatchString(project) {
		err = errors.New("local Docker project name must use lowercase letters, numbers, underscore, or hyphen")
		return
	}
	dataVolume = candidate.Configuration["dataVolume"]
	if !dockerProjectPattern.MatchString(dataVolume) {
		err = errors.New("local Docker data volume must use lowercase letters, numbers, underscore, or hyphen")
		return
	}
	hostIP, hostPort, err = net.SplitHostPort(candidate.Configuration["listenAddress"])
	if err != nil || hostIP != "127.0.0.1" {
		err = errors.New("local Docker listenAddress must use 127.0.0.1 and an explicit port")
		return
	}
	if parsedPort, portErr := strconv.Atoi(hostPort); portErr != nil || parsedPort < 1024 || parsedPort > 65535 {
		err = errors.New("local Docker listenAddress port must be between 1024 and 65535")
	}
	return
}

func loopbackHealthClient(port string) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}
