package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(New("test-sha"))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := resp.Header.Get("Cache-Control"), "no-store"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}

	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if got := string(body[:n]); !strings.HasPrefix(got, "ok test-sha") {
		t.Errorf("body = %q, want prefix %q", got, "ok test-sha")
	}
}

func TestRoot(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(New("v"))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "airplanes.live") {
		t.Errorf("/ body missing 'airplanes.live' marker")
	}
}

func TestUnknownPath404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(New("v"))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/no-such-thing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHealthRejectsPost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(New("v"))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/health", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health status = %d, want 405", resp.StatusCode)
	}
}
