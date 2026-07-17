package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
	"golang.org/x/crypto/ssh"
)

type RemoteLinuxCompose struct{}

func NewRemoteLinuxCompose() *RemoteLinuxCompose { return &RemoteLinuxCompose{} }

func (a *RemoteLinuxCompose) Preflight(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) Report {
	report := Report{Target: candidate.Target.Kind}
	auth, err := sshAuthentication(credentials)
	if err != nil {
		appendCheck(&report, "ssh-authentication", false, err.Error())
		return report
	}
	expectedFingerprint := candidate.Configuration["hostKeySha256"]
	config := &ssh.ClientConfig{
		User: candidate.Configuration["user"], Auth: auth, Timeout: 30 * time.Second,
		HostKeyCallback: pinnedHostKey(expectedFingerprint),
	}
	address := net.JoinHostPort(candidate.Configuration["host"], candidate.Configuration["port"])
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		appendCheck(&report, "ssh-connection", false, "Ranch Hand could not reach the remote SSH service: "+err.Error())
		return report
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		appendCheck(&report, "ssh-host-identity", false, "SSH authentication or pinned host identity verification failed: "+err.Error())
		return report
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()
	appendCheck(&report, "ssh-host-identity", true, "Ranch Hand connected through native SSH and verified the pinned host identity.")

	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	dockerVersion, err := remoteCommand(client, `docker version --format '{{.Server.Version}}/{{.Server.Os}}/{{.Server.Arch}}'`)
	if err != nil || !strings.Contains(dockerVersion, "/linux/") {
		appendCheck(&report, "remote-docker-engine", false, "The remote account cannot reach a Linux Docker Engine.")
		return report
	}
	appendCheck(&report, "remote-docker-engine", true, "The remote account can reach a Linux Docker Engine ("+dockerVersion+").")
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	composeVersion, err := remoteCommand(client, `docker compose version --short`)
	if err != nil || composeVersion == "" {
		appendCheck(&report, "remote-docker-compose", false, "Docker Compose v2 is not available to the remote account.")
		return report
	}
	appendCheck(&report, "remote-docker-compose", true, "Docker Compose v2 is available ("+composeVersion+").")
	appendCheck(&report, "compose-https-boundary", true, "The verified bundle contains no proxy; public HTTPS remains an explicit operator-managed ingress.")
	report.Ready = true
	return report
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

func remoteCommand(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output := &limitedOutput{maximum: 64 << 10}
	session.Stdout = output
	session.Stderr = output
	err = session.Run(command)
	if output.truncated {
		return "", fmt.Errorf("remote command output exceeded 64 KiB")
	}
	return strings.TrimSpace(output.String()), err
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
