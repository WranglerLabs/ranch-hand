package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalDockerLoadsVerifiedArchiveThroughEngineAPI(t *testing.T) {
	loaded := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/images/load" || r.URL.Query().Get("quiet") != "1" {
			t.Fatalf("unexpected image-load request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Content-Type") != "application/x-tar" {
			t.Fatal("image archive content type was not set")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "verified-image-archive" {
			t.Fatal("Docker Engine did not receive the verified image archive")
		}
		loaded = true
		_, _ = io.WriteString(w, "{\"stream\":\"Loaded image\"}\n")
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if err := adapter.loadImageArchive(context.Background(), strings.NewReader("verified-image-archive")); err != nil {
		t.Fatal(err)
	}
	if !loaded {
		t.Fatal("verified image archive was not loaded")
	}
}

func TestLocalDockerRejectsEngineImageLoadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{\"errorDetail\":{\"message\":\"invalid archive\"}}\n")
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if err := adapter.loadImageArchive(context.Background(), strings.NewReader("bad")); err == nil {
		t.Fatal("Docker Engine image-load failure was accepted")
	}
}
