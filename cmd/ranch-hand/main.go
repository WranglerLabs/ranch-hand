package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/server"
	"github.com/WranglerLabs/ranch-hand/internal/ui"
)

var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Ranch Hand: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open loopback listener: %w", err)
	}
	token, err := newToken()
	if err != nil {
		return fmt.Errorf("create launch token: %w", err)
	}

	httpServer := server.DefaultHTTPServer(listener.Addr().String(), server.New(token, version, ui.Files()))
	serveError := make(chan error, 1)
	go func() { serveError <- httpServer.Serve(listener) }()

	launchURL := fmt.Sprintf("http://%s/#token=%s", listener.Addr().String(), token)
	baseURL := fmt.Sprintf("http://%s/", listener.Addr().String())
	fmt.Printf("Ranch Hand %s is ready on %s\n", version, baseURL)
	if os.Getenv("RANCH_HAND_NO_BROWSER") == "1" {
		fmt.Printf("Test launch URL: %s\n", launchURL)
	} else if err := openBrowser(launchURL); err != nil {
		fmt.Printf("Could not open the default browser (%v).\n", err)
		fmt.Printf("Session URL: %s\n", launchURL)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func newToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	return command.Start()
}
