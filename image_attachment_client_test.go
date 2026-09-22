package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type attachmentTestTransport func(*http.Request) (*http.Response, error)

func (f attachmentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func attachmentTestResponse(status int, data any) *http.Response {
	body, _ := json.Marshal(data)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}

type attachmentFixture struct {
	t             *testing.T
	client        *imageAttachmentClient
	objects       map[string][]byte
	puts          int
	releases      int
	verifications int
	rpcs          []string
	releaseAfter  int
	verifyStatus  int
	putStatus     int
	putError      bool
	lostPresign   bool
	getMismatch   bool
	putURL        string
	blockPUT      <-chan struct{}
	startedPUT    chan<- struct{}
}

func newAttachmentFixture(t *testing.T, org string) *attachmentFixture {
	f := &attachmentFixture{t: t, objects: map[string][]byte{}, releaseAfter: 1, putStatus: http.StatusOK}
	f.client = newImageAttachmentClient("secret-key", org)
	f.client.transport = attachmentTestTransport(f.roundTrip)
	return f
}

func (f *attachmentFixture) signed(key string) map[string]any {
	return map[string]any{"key": key, "signedUrl": "https://account.r2.cloudflarestorage.com/bucket/" + key + "?X-Amz-Signature=private-signature", "expiresAt": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)}
}

func (f *attachmentFixture) roundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "api.kilo.ai" {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer secret-key" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Cookie") != "" {
			f.t.Error("incorrect attachment RPC credentials or method")
		}
		procedure := strings.TrimPrefix(r.URL.Path, "/api/trpc/")
		f.rpcs = append(f.rpcs, procedure)
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			f.t.Error(err)
		}
		if strings.HasPrefix(procedure, "organizations.") {
			if input["organizationId"] != f.client.org || f.client.org == "" {
				f.t.Error("wrong organization admission")
			}
		} else if input["organizationId"] != nil {
			f.t.Error("personal download should not include organization")
		}
		procedure = strings.TrimPrefix(procedure, "organizations.")
		var output any
		switch procedure {
		case "cloudAgentNext.getAttachmentUploadUrl":
			message, _ := input["messageUuid"].(string)
			id, _ := input["attachmentId"].(string)
			if len(message) != 36 || len(id) != 36 || message == id || message[14] != '4' || id[14] != '4' {
				f.t.Error("invalid random UUID identities")
			}
			key := "user-id/cloud-agent/" + message + "/" + id + ".png"
			if f.lostPresign {
				return nil, errors.New("secret-key upstream response lost")
			}
			result := f.signed(key)
			if f.putURL != "" {
				result["signedUrl"] = f.putURL
			}
			output = result
		case "cloudAgentNext.getAttachmentDownloadUrl":
			key := "user-id/cloud-agent/" + input["messageUuid"].(string) + "/" + input["filename"].(string)
			if f.getMismatch {
				key = "other-user/" + key
			}
			output = f.signed(key)
		case "cloudAgentNext.releasePendingUploads":
			f.releases++
			if f.releases >= f.releaseAfter {
				for _, key := range input["objectKeys"].([]any) {
					delete(f.objects, key.(string))
				}
			}
			output = map[string]bool{"success": true}
		default:
			f.t.Fatalf("unexpected RPC procedure %s", procedure)
		}
		return attachmentTestResponse(http.StatusOK, map[string]any{"result": map[string]any{"data": output}}), nil
	}
	if r.URL.Host != "account.r2.cloudflarestorage.com" {
		f.t.Fatal("request escaped permitted storage host")
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-KiloCode-OrganizationId") != "" {
		f.t.Error("credentials leaked to storage")
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	switch r.Method {
	case http.MethodPut:
		if f.startedPUT != nil {
			f.startedPUT <- struct{}{}
		}
		if f.blockPUT != nil {
			<-f.blockPUT
		}
		f.puts++
		data, _ := io.ReadAll(r.Body)
		if r.ContentLength != int64(len(data)) || r.Header.Get("Content-Type") != "image/png" {
			f.t.Error("PUT length or MIME mismatch")
		}
		if f.putStatus == http.StatusOK {
			f.objects[key] = data
		}
		if f.putError {
			return nil, errors.New("private-signature uncertain PUT")
		}
		return attachmentTestResponse(f.putStatus, map[string]any{}), nil
	case http.MethodGet:
		f.verifications++
		if r.Header.Get("Range") != "bytes=0-0" {
			f.t.Error("verification GET must be bounded")
		}
		status := f.verifyStatus
		if status == 0 {
			status = http.StatusNotFound
			if _, ok := f.objects[key]; ok {
				status = http.StatusPartialContent
			}
		}
		return attachmentTestResponse(status, map[string]any{}), nil
	default:
		f.t.Fatal("unexpected storage operation")
	}
	return nil, errors.New("unreachable")
}

func TestImageAttachmentClientOriginalBytesScopeAndDeletion(t *testing.T) {
	for _, org := range []string{"", "team-id"} {
		t.Run(org, func(t *testing.T) {
			f := newAttachmentFixture(t, org)
			lease, err := f.client.NewLease()
			if err != nil {
				t.Fatal(err)
			}
			original := []byte{0, 128, 1, 2, 255, 3}
			for range 2 {
				url, err := lease.Upload(context.Background(), original, "image/png")
				if err != nil || !strings.Contains(url, "X-Amz-Signature=") {
					t.Fatalf("upload failed: %v", err)
				}
			}
			if len(f.objects) != 2 || f.puts != 2 || time.Until(lease.ExpiresAt()) < 14*time.Minute {
				t.Fatal("invalid lease state")
			}
			for _, data := range f.objects {
				if !bytes.Equal(data, original) {
					t.Fatal("original bytes changed")
				}
			}
			if err := lease.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(f.objects) != 0 || f.releases != 1 || f.verifications != 2 {
				t.Fatal("cleanup did not delete and independently verify every original")
			}
			if err := lease.Close(context.Background()); err != nil || f.releases != 1 {
				t.Fatal("completed cleanup should be idempotent")
			}
			if _, err := lease.Upload(context.Background(), original, "image/png"); err == nil {
				t.Fatal("closed lease accepted new image")
			}
			prefix := ""
			if org != "" {
				prefix = "organizations."
			}
			if f.rpcs[0] != prefix+"cloudAgentNext.getAttachmentUploadUrl" || f.rpcs[1] != "cloudAgentNext.getAttachmentDownloadUrl" || f.rpcs[4] != prefix+"cloudAgentNext.releasePendingUploads" {
				t.Fatalf("wrong personal/organization routing: %v", f.rpcs)
			}
		})
	}
}

func TestImageAttachmentClientRejectsLimitsWithoutNetwork(t *testing.T) {
	f := newAttachmentFixture(t, "")
	lease, _ := f.client.NewLease()
	for _, tc := range []struct {
		data []byte
		mime string
	}{{nil, "image/png"}, {[]byte{1}, "text/plain"}, {make([]byte, imageAttachmentMaxBytes+1), "image/png"}} {
		if _, err := lease.Upload(context.Background(), tc.data, tc.mime); err == nil {
			t.Error("invalid attachment accepted")
		}
	}
	if len(f.rpcs) != 0 {
		t.Fatal("invalid attachment sent to Kilo")
	}
	for range imageAttachmentMaxCount {
		if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err == nil || f.puts != imageAttachmentMaxCount {
		t.Fatal("per-message quota was bypassed")
	}
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestImageAttachmentURLAndKeyValidation(t *testing.T) {
	key := "user/cloud-agent/message/file.png"
	base := "https://account.r2.cloudflarestorage.com/bucket/" + key + "?signature=secret"
	if !validImageAttachmentURL(base, key) {
		t.Fatal("valid R2 URL rejected")
	}
	for _, invalid := range []string{
		strings.Replace(base, "https:", "http:", 1), strings.Replace(base, "account.", "user@account.", 1), strings.Replace(base, ".com/", ".com:443/", 1),
		strings.Replace(base, ".com/", ".com:/", 1),
		strings.Replace(base, "cloudflarestorage.com", "cloudflarestorage.com.evil.example", 1), strings.Replace(base, "account.r2.cloudflarestorage.com", "127.0.0.1", 1),
		strings.Replace(base, "/bucket/", "/bucket/../other/", 1), strings.Replace(base, "/file.png", "/other.png", 1), base + "#fragment", base + strings.Repeat("x", 4096),
	} {
		if validImageAttachmentURL(invalid, key) {
			t.Error("unsafe signed URL accepted")
		}
	}
	for _, invalid := range []string{"/cloud-agent/message/file.png", "../cloud-agent/message/file.png", "user/cloud-agent/other/file.png", "user/cloud-agent/message/other.png", "user\\other/cloud-agent/message/file.png", "user%2Fother/cloud-agent/message/file.png"} {
		if validImageAttachmentKey(invalid, "message", "file.png") {
			t.Error("unsafe object identity accepted")
		}
	}
}

func TestImageAttachmentFailureStillReleasesTrackedIdentity(t *testing.T) {
	for _, scenario := range []string{"invalid URL", "lost presign", "HTTP PUT failure", "download mismatch", "ambiguous PUT"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAttachmentFixture(t, "team-id")
			switch scenario {
			case "invalid URL":
				f.putURL = "https://attacker.example/image"
			case "lost presign":
				f.lostPresign = true
			case "HTTP PUT failure":
				f.putStatus = 503
			case "download mismatch":
				f.getMismatch = true
			case "ambiguous PUT":
				f.putError = true
			}
			lease, _ := f.client.NewLease()
			if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err == nil {
				t.Fatal("failed upload accepted")
			} else if strings.Contains(err.Error(), "private-signature") || strings.Contains(err.Error(), "secret-key") {
				t.Fatal("error leaked secret")
			}
			err := lease.Close(context.Background())
			if f.releases == 0 || len(f.objects) != 0 {
				t.Fatal("partial upload identity was not cleaned up")
			}
			wantError := scenario == "download mismatch" || scenario == "ambiguous PUT"
			if (err != nil) != wantError {
				t.Fatalf("incorrect cleanup certainty for %s: %v", scenario, err)
			}
		})
	}
}

func TestImageAttachmentDeletionRequires404NotRPCSuccessOr403(t *testing.T) {
	for _, scenario := range []string{"release does nothing", "signature expired", "deletes on retry"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAttachmentFixture(t, "")
			switch scenario {
			case "release does nothing":
				f.releaseAfter = 99
			case "signature expired":
				f.verifyStatus = 403
			case "deletes on retry":
				f.releaseAfter = 2
			}
			lease, _ := f.client.NewLease()
			if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err != nil {
				t.Fatal(err)
			}
			err := lease.Close(context.Background())
			if scenario == "deletes on retry" {
				if err != nil || f.releases != 2 {
					t.Fatal("bounded deletion retry failed")
				}
			} else if err == nil || f.releases != 3 || f.verifications != 3 {
				t.Fatal("unconfirmed deletion reported as complete")
			}
		})
	}
}

func TestImageAttachmentCloseWaitsForUploadAndCanRetryAfterCancellation(t *testing.T) {
	f := newAttachmentFixture(t, "")
	putStarted := make(chan struct{})
	putFinish := make(chan struct{})
	f.startedPUT, f.blockPUT = putStarted, putFinish
	lease, _ := f.client.NewLease()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err != nil {
			t.Error(err)
		}
	}()
	<-putStarted
	closed := make(chan error, 1)
	go func() { closed <- lease.Close(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("cleanup raced a pending PUT")
	case <-time.After(30 * time.Millisecond):
	}
	close(putFinish)
	wg.Wait()
	if err := <-closed; err != nil || len(f.objects) != 0 {
		t.Fatal("cleanup after pending PUT failed")
	}
	second, _ := f.client.NewLease()
	f.blockPUT, f.startedPUT = nil, nil
	if _, err := second.Upload(context.Background(), []byte{1}, "image/png"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := second.Close(ctx); err == nil {
		t.Fatal("cancelled cleanup reported success")
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal("cleanup retry lost identities")
	}
}

func TestImageAttachmentNoRedirectOrAmbientProxyAndBoundedRPC(t *testing.T) {
	c := newImageAttachmentClient("secret-key", "")
	if c.transport.(*http.Transport).Proxy != nil || c.httpClient().Timeout != 60*time.Second {
		t.Fatal("attachment requests inherit ambient proxy or have no timeout")
	}
	for _, scenario := range []string{"redirect", "oversized", "error envelope"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			c.transport = attachmentTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("redirect followed")
				}
				resp := attachmentTestResponse(200, map[string]any{"error": map[string]any{"message": "private-signature secret-key"}})
				switch scenario {
				case "redirect":
					resp.StatusCode = 307
					resp.Header.Set("Location", "https://attacker.example/")
				case "oversized":
					resp.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", imageAttachmentResponseLimit+1)))
				}
				return resp, nil
			})
			lease, _ := c.NewLease()
			_, err := lease.Upload(context.Background(), []byte{1}, "image/png")
			if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "private-signature") || calls != 1 {
				t.Fatal("RPC failure/size/redirect was not contained")
			}
		})
	}
}

func TestImageAttachmentCleanupRefreshesReadURLsAndVerifiesDespiteReleaseFailure(t *testing.T) {
	f := newAttachmentFixture(t, "team-id")
	lease, _ := f.client.NewLease()
	if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err != nil {
		t.Fatal(err)
	}
	lease.objects[0].expires = time.Now().Add(-time.Minute)
	base := f.client.transport
	f.client.transport = attachmentTestTransport(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if strings.HasSuffix(r.URL.Path, ".releasePendingUploads") {
			response.Body.Close()
			// Simulate a lost response after the backend already deleted data.
			return nil, errors.New("release response lost")
		}
		return response, err
	})
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal("verified deletion was not accepted after lost release response")
	}
	if len(f.rpcs) != 4 || f.rpcs[2] != "cloudAgentNext.getAttachmentDownloadUrl" || f.verifications != 1 {
		t.Fatal("expired signature was not refreshed before cleanup")
	}
}

func TestImageAttachmentReadURLMustIdentifyOriginalStorageLocation(t *testing.T) {
	for _, mismatch := range []string{"host", "bucket"} {
		t.Run(mismatch, func(t *testing.T) {
			f := newAttachmentFixture(t, "")
			base := f.client.transport
			f.client.transport = attachmentTestTransport(func(r *http.Request) (*http.Response, error) {
				resp, err := base.RoundTrip(r)
				if strings.HasSuffix(r.URL.Path, ".getAttachmentDownloadUrl") {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if mismatch == "host" {
						body = bytes.ReplaceAll(body, []byte("account.r2.cloudflarestorage.com"), []byte("other.r2.cloudflarestorage.com"))
					} else {
						body = bytes.ReplaceAll(body, []byte("/bucket/"), []byte("/wrong-bucket/"))
					}
					resp.Body = io.NopCloser(bytes.NewReader(body))
				}
				return resp, err
			})
			lease, _ := f.client.NewLease()
			if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err == nil {
				t.Fatal("read URL from another storage location accepted")
			}
			if err := lease.Close(context.Background()); err == nil || f.verifications != 0 || f.releases != 3 || len(f.objects) != 0 {
				t.Fatal("cleanup used another bucket's 404 as proof of deletion")
			}
		})
	}
}

func TestImageAttachmentExpiredReadURLCannotConfirmDeletion(t *testing.T) {
	f := newAttachmentFixture(t, "")
	lease, _ := f.client.NewLease()
	if _, err := lease.Upload(context.Background(), []byte{1}, "image/png"); err != nil {
		t.Fatal(err)
	}
	lease.objects[0].expires = time.Now().Add(-time.Minute)
	base := f.client.transport
	f.client.transport = attachmentTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, ".getAttachmentDownloadUrl") {
			return nil, errors.New("read URL refresh unavailable")
		}
		return base.RoundTrip(r)
	})
	if err := lease.Close(context.Background()); err == nil || f.verifications != 0 || len(f.objects) != 0 {
		t.Fatal("expired URL was used to confirm deletion")
	}
}
