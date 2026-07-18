package adapter

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
	"golang.org/x/crypto/ssh"
)

type remoteHost interface {
	Run(context.Context, string, []byte) (string, error)
	Health(context.Context, string) (int, []byte, error)
	Close() error
}

type remoteConnector func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error)

type RemoteLinuxCompose struct {
	connect remoteConnector
}

type SSHHostIdentity struct {
	Algorithm   string `json:"algorithm"`
	Fingerprint string `json:"fingerprint"`
}

var errSSHHostKeyCaptured = errors.New("SSH host key captured")

// InspectSSHHostKey reads the identity presented by an SSH service without
// sending user credentials. The caller must verify this first-seen fingerprint
// through a trusted server console or administrator before pinning it.
func InspectSSHHostKey(ctx context.Context, host, port string) (SSHHostIdentity, error) {
	if err := plan.ValidateRemoteSSHEndpoint(host, port); err != nil {
		return SSHHostIdentity{}, err
	}
	address := net.JoinHostPort(host, port)
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return SSHHostIdentity{}, errors.New("Ranch Hand could not reach the remote SSH service")
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	var identity SSHHostIdentity
	config := &ssh.ClientConfig{
		User: "ranch-hand-host-key-inspection",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			identity = SSHHostIdentity{Algorithm: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)}
			return errSSHHostKeyCaptured
		},
		Timeout: 30 * time.Second,
	}
	_, _, _, _ = ssh.NewClientConn(connection, address, config)
	if identity.Fingerprint == "" {
		return SSHHostIdentity{}, errors.New("the remote service did not present a supported SSH host key")
	}
	return identity, nil
}

func NewRemoteLinuxCompose() *RemoteLinuxCompose {
	return &RemoteLinuxCompose{connect: connectRemoteLinux}
}

func newRemoteLinuxCompose(connect remoteConnector) *RemoteLinuxCompose {
	return &RemoteLinuxCompose{connect: connect}
}

func (a *RemoteLinuxCompose) Preflight(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) Report {
	report := Report{Target: candidate.Target.Kind}
	if err := candidate.Validate(); err != nil {
		appendCheck(&report, "remote-plan", false, err.Error())
		return report
	}
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		appendCheck(&report, "ssh-host-identity", false, err.Error())
		return report
	}
	defer host.Close()
	appendCheck(&report, "ssh-host-identity", true, "Ranch Hand connected through native SSH and verified the pinned host identity.")

	if _, err := host.Run(ctx, `command -v docker`, nil); err != nil {
		report.State = "prerequisites-installable"
		appendCheck(&report, "remote-docker-command", false, "Docker Engine is not installed. Ranch Hand can install Docker Engine and Compose on a supported Ubuntu/Debian server using sudo.")
		return report
	}
	appendCheck(&report, "remote-docker-command", true, "The Docker command is installed on the remote Linux server.")
	dockerVersion, err := host.Run(ctx, `docker version --format '{{.Server.Version}}/{{.Server.Os}}/{{.Server.Arch}}'`, nil)
	if err != nil || !strings.Contains(dockerVersion, "/linux/") {
		report.State = "prerequisites-installable"
		appendCheck(&report, "remote-docker-engine", false, "Docker is installed, but its Engine is stopped or this account lacks access. Ranch Hand can repair the supported Ubuntu/Debian prerequisite setup using sudo.")
		return report
	}
	appendCheck(&report, "remote-docker-engine", true, "The remote account can reach a Linux Docker Engine ("+dockerVersion+").")
	composeVersion, err := host.Run(ctx, `docker compose version --short`, nil)
	if err != nil || composeVersion == "" {
		report.State = "prerequisites-installable"
		appendCheck(&report, "remote-docker-compose", false, "Docker Compose v2 is not available. Ranch Hand can install it on a supported Ubuntu/Debian server using sudo.")
		return report
	}
	appendCheck(&report, "remote-docker-compose", true, "Docker Compose v2 is available ("+composeVersion+").")
	directory := shellQuote(candidate.Configuration["installDirectory"])
	if _, err := host.Run(ctx, "test ! -e "+directory+" && test -d "+shellQuote(filepathParent(candidate.Configuration["installDirectory"]))+" && test -w "+shellQuote(filepathParent(candidate.Configuration["installDirectory"])), nil); err != nil {
		appendCheck(&report, "remote-install-directory", false, "The dedicated installation directory already exists or its parent is not writable.")
		return report
	}
	appendCheck(&report, "remote-install-directory", true, "The dedicated installation directory is unused and its parent is writable.")
	project := shellQuote(candidate.Configuration["projectName"])
	containers, err := host.Run(ctx, "docker ps --all --quiet --filter label=com.docker.compose.project="+project, nil)
	if err != nil || containers != "" {
		appendCheck(&report, "remote-compose-boundary", false, "The requested remote Compose project already has containers or cannot be inspected.")
		return report
	}
	volumes, err := host.Run(ctx, "docker volume ls --quiet --filter label=com.docker.compose.project="+project, nil)
	if err != nil || volumes != "" {
		appendCheck(&report, "remote-compose-boundary", false, "The requested remote Compose project already has volumes or cannot be inspected.")
		return report
	}
	appendCheck(&report, "remote-compose-boundary", true, "The requested Compose project has no existing containers or volumes.")
	appendCheck(&report, "compose-https-boundary", true, "The evaluation install remains loopback-only on the Linux host and contains no proxy or public ingress.")
	report.Ready = true
	return report
}

func (a *RemoteLinuxCompose) InstallPrerequisites(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		return err
	}
	defer host.Close()
	user := candidate.Configuration["user"]
	script := dockerPrerequisiteScript(user)
	if user == "root" {
		_, err = host.Run(ctx, "sh -s", []byte(script))
		return err
	}
	if _, sudoErr := host.Run(ctx, "sudo -n true", nil); sudoErr == nil {
		_, err = host.Run(ctx, "sudo -n sh -s", []byte(script))
		return err
	}
	password := credentials.SudoPassword
	if password == "" {
		password = credentials.SSHPassword
	}
	if password == "" {
		return errors.New("Docker installation requires this Linux account's sudo password or passwordless sudo")
	}
	_, err = host.Run(ctx, "sudo -S -p '' sh -s", []byte(password+"\n"+script))
	return err
}

func connectRemoteLinux(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) (remoteHost, error) {
	auth, err := sshAuthentication(credentials)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(candidate.Configuration["host"], candidate.Configuration["port"])
	expectedFingerprint := candidate.Configuration["hostKeySha256"]
	presentedFingerprint := ""
	hostIdentityVerified := false
	config := &ssh.ClientConfig{
		User: candidate.Configuration["user"], Auth: auth, Timeout: 30 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presentedFingerprint = ssh.FingerprintSHA256(key)
			if presentedFingerprint != expectedFingerprint {
				return errors.New("SSH host identity does not match the deployment plan")
			}
			hostIdentityVerified = true
			return nil
		},
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, errors.New("Ranch Hand could not reach the remote SSH service")
	}
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		_ = connection.Close()
		if presentedFingerprint != "" && !hostIdentityVerified {
			return nil, fmt.Errorf("SSH host identity mismatch: the server presented %s but the deployment plan pins %s", presentedFingerprint, expectedFingerprint)
		}
		if hostIdentityVerified {
			return nil, fmt.Errorf("SSH authentication failed for Linux user %q after the pinned server identity was verified; use the password accepted by SSH itself or select the private key SSH normally uses", candidate.Configuration["user"])
		}
		return nil, errors.New("the SSH handshake failed before Ranch Hand could verify the server identity")
	}
	_ = connection.SetDeadline(time.Time{})
	return &sshRemoteHost{client: ssh.NewClient(clientConnection, channels, requests), connection: connection}, nil
}

func pinnedHostKey(expectedFingerprint string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != expectedFingerprint {
			return errors.New("SSH host identity does not match the deployment plan")
		}
		return nil
	}
}

func sshAuthentication(credentials Credentials) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if credentials.SSHPrivateKey != "" {
		var signer ssh.Signer
		var err error
		if credentials.SSHPrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(credentials.SSHPrivateKey), []byte(credentials.SSHPrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(credentials.SSHPrivateKey))
		}
		if err != nil {
			return nil, errors.New("the in-memory SSH private key could not be parsed")
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if credentials.SSHPassword != "" {
		methods = append(methods, ssh.Password(credentials.SSHPassword))
	}
	if len(methods) == 0 {
		return nil, errors.New("an SSH private key or password is required in memory for remote preflight")
	}
	return methods, nil
}

type sshRemoteHost struct {
	client     *ssh.Client
	connection net.Conn
}

func (h *sshRemoteHost) Run(ctx context.Context, command string, stdin []byte) (string, error) {
	session, err := h.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	// Keep the SSH execution request constant. Plan-derived values are validated
	// and quoted by the typed operations that build command; transferring the
	// resulting script over stdin prevents those values from becoming the SSH
	// command itself. Optional file content is quoted as data and piped to the
	// internal command, never concatenated unquoted into the script.
	script := remoteShellScript(command, stdin)
	session.Stdin = strings.NewReader(script)
	output := &limitedOutput{maximum: 64 << 10}
	session.Stdout = output
	session.Stderr = output
	if err := session.Start("sh -s"); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = session.Close()
		<-done
		err = ctx.Err()
	}
	if output.truncated {
		return "", fmt.Errorf("remote command output exceeded 64 KiB")
	}
	return strings.TrimSpace(output.String()), err
}

func remoteShellScript(command string, stdin []byte) string {
	if len(stdin) == 0 {
		return command + "\n"
	}
	// Keep a compound command in one pipeline consumer. Without the subshell,
	// "printf | umask; cat" feeds umask and leaves cat with an empty stream.
	return "printf '%s' " + shellQuote(string(stdin)) + " | ( " + command + " )\n"
}

func (h *sshRemoteHost) Health(ctx context.Context, requestPath string) (int, []byte, error) {
	const address = "127.0.0.1:8080"
	connection, err := h.client.Dial("tcp", address)
	if err != nil {
		return 0, nil, err
	}
	defer connection.Close()
	deadline := time.Now().Add(30 * time.Second)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = connection.SetDeadline(deadline)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+requestPath, nil)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Connection", "close")
	if err := request.Write(connection); err != nil {
		return 0, nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxControlPlaneResponse+1))
	if err != nil || len(body) > maxControlPlaneResponse {
		return response.StatusCode, nil, errors.New("remote health response exceeded the safety limit")
	}
	return response.StatusCode, body, nil
}

func (h *sshRemoteHost) Close() error {
	clientErr := h.client.Close()
	connectionErr := h.connection.Close()
	return errors.Join(clientErr, connectionErr)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func filepathParent(value string) string {
	index := strings.LastIndex(value, "/")
	if index <= 0 {
		return "/"
	}
	return value[:index]
}

type limitedOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	maximum   int
	truncated bool
}

func (w *limitedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.maximum - w.buffer.Len()
	if remaining <= 0 {
		w.truncated = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = w.buffer.Write(value[:remaining])
		w.truncated = true
		return len(value), nil
	}
	_, _ = w.buffer.Write(value)
	return len(value), nil
}

func (w *limitedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}
