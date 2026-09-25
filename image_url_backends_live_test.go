package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in only. Publishes a generated three-by-two PNG, never user files or Kilo
// credentials. Litterbox has no early deletion; the tiny upload expires in 1h.
func TestImageURLBackendLive(t *testing.T) {
	backend := os.Getenv("KILO_IMAGE_BACKEND_LIVE")
	if backend == "" {
		t.Skip("set KILO_IMAGE_BACKEND_LIVE=cloudflare, litterbox, or tailscale")
	}
	if backend != "cloudflare" && backend != "litterbox" && backend != "tailscale" {
		t.Fatal("invalid KILO_IMAGE_BACKEND_LIVE")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var manager imageURLBackendManager
	defer manager.Close()
	lease, err := manager.NewLease(ctx, backend, "1h")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close(context.Background())
	original := imageURLTestPNG(t)
	publicURL, err := lease.Upload(ctx, original, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	client := imageURLHTTPClient()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, publicURL, nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("public synthetic image could not be downloaded")
	}
	downloaded, readErr := io.ReadAll(io.LimitReader(response.Body, int64(len(original)+1)))
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !bytes.Equal(downloaded, original) {
		t.Fatal("public synthetic image differs from original bytes")
	}
	if err = lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend != "litterbox" {
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, publicURL, nil)
		response, err = client.Do(req)
		if err != nil {
			t.Fatal("could not verify removal through the still-running tunnel")
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("closed lease image still accessible: HTTP %d", response.StatusCode)
		}
	}
	if err = manager.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("Synthetic PNG byte comparison and backend cleanup passed; no inference or credentials used.")
}
