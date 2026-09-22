package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	imageAttachmentMaxBytes      = 20 << 20
	imageAttachmentMaxCount      = 5
	imageAttachmentResponseLimit = 64 << 10
	imageAttachmentEndpoint      = "https://api.kilo.ai/api/trpc/"
)

var (
	errImageAttachment        = errors.New("Kilo's experimental image upload failed; no image quality was reduced")
	errImageAttachmentCleanup = errors.New("Could not confirm deletion of every temporary image from Kilo; temporary URLs expiring does not delete stored images")
)

// This client uses Kilo's Cloud Agent attachment API, which is not a public
// Gateway upload contract. Credentials and presigned URLs must never be logged.
// endpoint and transport are internal test seams, not user-configurable hosts.
type imageAttachmentClient struct {
	endpoint  string
	key       string
	org       string
	transport http.RoundTripper
}

func newImageAttachmentClient(key, org string) *imageAttachmentClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &imageAttachmentClient{endpoint: imageAttachmentEndpoint, key: key, org: org, transport: transport}
}

type imageAttachmentLease struct {
	mu      sync.Mutex
	client  *imageAttachmentClient
	message string
	objects []*imageAttachmentObject
	closed  bool
}

type imageAttachmentObject struct {
	filename     string
	key          string
	storageHost  string
	storagePath  string
	getURL       string
	expires      time.Time
	deleted      bool
	ambiguousPUT bool
}

type imageAttachmentSigned struct {
	URL     string `json:"signedUrl"`
	Key     string `json:"key"`
	Expires string `json:"expiresAt"`
}

func imageAttachmentUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errImageAttachment
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (c *imageAttachmentClient) NewLease() (*imageAttachmentLease, error) {
	if c.key == "" {
		return nil, errImageAttachment
	}
	message, err := imageAttachmentUUID()
	if err != nil {
		return nil, err
	}
	return &imageAttachmentLease{client: c, message: message}, nil
}

func (c *imageAttachmentClient) httpClient() *http.Client {
	return &http.Client{Transport: c.transport, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *imageAttachmentClient) rpc(ctx context.Context, procedure string, input map[string]any, organization bool, output any) error {
	if organization && c.org != "" {
		procedure = "organizations." + procedure
		input["organizationId"] = c.org
	}
	body, err := json.Marshal(input)
	if err != nil {
		return errImageAttachment
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+procedure, bytes.NewReader(body))
	if err != nil {
		return errImageAttachment
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return errImageAttachment
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, imageAttachmentResponseLimit+1))
	if err != nil || len(body) > imageAttachmentResponseLimit || resp.StatusCode != http.StatusOK {
		return errImageAttachment
	}
	var envelope struct {
		Result struct {
			Data json.RawMessage `json:"data"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) != 0 || len(envelope.Result.Data) == 0 || bytes.Equal(envelope.Result.Data, []byte("null")) || json.Unmarshal(envelope.Result.Data, output) != nil {
		return errImageAttachment
	}
	return nil
}

func validImageAttachmentKey(key, message, filename string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || len(parts[0]) == 0 || len(parts[0]) > 256 || parts[0] == "." || parts[0] == ".." || parts[1] != "cloud-agent" || parts[2] != message || parts[3] != filename {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("\\?#%", r) {
			return false
		}
	}
	return true
}

func validImageAttachmentURL(raw, key string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.User != nil || strings.Contains(u.Host, ":") || u.Fragment != "" || u.Opaque != "" || u.RawQuery == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	const suffix = ".r2.cloudflarestorage.com"
	if !strings.HasSuffix(host, suffix) || len(host) == len(suffix) {
		return false
	}
	prefix, ok := strings.CutSuffix(u.Path, "/"+key)
	if !ok {
		return false
	}
	// R2 may use a path-style bucket or a virtual-hosted bucket. An arbitrary
	// extra path, traversal, or mismatched key is never accepted.
	return prefix == "" || (strings.HasPrefix(prefix, "/") && len(prefix) > 1 && !strings.Contains(prefix[1:], "/") && prefix != "/." && prefix != "/..")
}

func attachmentExpiry(value string) (time.Time, bool) {
	expires, err := time.Parse(time.RFC3339, value)
	now := time.Now()
	return expires, err == nil && expires.After(now.Add(time.Minute)) && expires.Before(now.Add(16*time.Minute))
}

// Upload is synchronous and serialized with Close. In particular Close never
// releases an object while this client still has its PUT in flight.
func (l *imageAttachmentLease) Upload(ctx context.Context, data []byte, mime string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/gif": "gif"}[mime]
	if l.closed || len(l.objects) >= imageAttachmentMaxCount || len(data) == 0 || len(data) > imageAttachmentMaxBytes || ext == "" {
		return "", errImageAttachment
	}
	id, err := imageAttachmentUUID()
	if err != nil {
		return "", err
	}
	object := &imageAttachmentObject{filename: id + "." + ext}
	// Keep the generated identity even if the presign response is lost: Close
	// can resolve the canonical key before releasing a possibly admitted row.
	l.objects = append(l.objects, object)
	var signed imageAttachmentSigned
	err = l.client.rpc(ctx, "cloudAgentNext.getAttachmentUploadUrl", map[string]any{
		"messageUuid": l.message, "attachmentId": id, "contentType": mime, "contentLength": len(data),
	}, true, &signed)
	if err != nil {
		return "", err
	}
	if !validImageAttachmentKey(signed.Key, l.message, object.filename) {
		return "", errImageAttachment
	}
	object.key = signed.Key
	if _, ok := attachmentExpiry(signed.Expires); !ok || !validImageAttachmentURL(signed.URL, object.key) {
		return "", errImageAttachment
	}
	storageURL, _ := url.Parse(signed.URL)
	object.storageHost, object.storagePath = strings.ToLower(storageURL.Host), storageURL.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.URL, bytes.NewReader(data))
	if err != nil {
		return "", errImageAttachment
	}
	req.Header.Set("Content-Type", mime)
	resp, err := l.client.httpClient().Do(req)
	if err != nil {
		// A server may finish a PUT after the connection is lost. A subsequent
		// immediate 404 alone cannot prove it will never finish writing.
		object.ambiguousPUT = true
		return "", errImageAttachment
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, imageAttachmentResponseLimit+1))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || readErr != nil {
		return "", errImageAttachment
	}
	if err := l.downloadURL(ctx, object); err != nil {
		return "", err
	}
	return object.getURL, nil
}

func (l *imageAttachmentLease) downloadURL(ctx context.Context, object *imageAttachmentObject) error {
	var signed imageAttachmentSigned
	if err := l.client.rpc(ctx, "cloudAgentNext.getAttachmentDownloadUrl", map[string]any{"messageUuid": l.message, "filename": object.filename}, false, &signed); err != nil {
		return err
	}
	if !validImageAttachmentKey(signed.Key, l.message, object.filename) || (object.key != "" && object.key != signed.Key) {
		return errImageAttachment
	}
	object.key = signed.Key
	expires, ok := attachmentExpiry(signed.Expires)
	if !ok || !validImageAttachmentURL(signed.URL, object.key) {
		return errImageAttachment
	}
	storageURL, _ := url.Parse(signed.URL)
	if object.storageHost != "" && (object.storageHost != strings.ToLower(storageURL.Host) || object.storagePath != storageURL.Path) {
		return errImageAttachment
	}
	object.storageHost, object.storagePath = strings.ToLower(storageURL.Host), storageURL.Path
	object.getURL, object.expires = signed.URL, expires
	return nil
}

// ExpiresAt is the earliest expiry of any URL returned by this lease. Callers
// should bound inference to finish before it; expiring URLs do not delete data.
func (l *imageAttachmentLease) ExpiresAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	var first time.Time
	for _, object := range l.objects {
		if !object.expires.IsZero() && (first.IsZero() || object.expires.Before(first)) {
			first = object.expires
		}
	}
	return first
}

// Close must receive an independent, bounded cleanup context, even if inference
// was cancelled. It is safe to call again after an unconfirmed cleanup. No
// credentials, object keys, URLs or original image bytes are persisted to disk.
func (l *imageAttachmentLease) Close(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return errImageAttachmentCleanup
		}
		keys := make([]string, 0, len(l.objects))
		for _, object := range l.objects {
			if object.deleted {
				continue
			}
			if object.key == "" || object.getURL == "" || time.Until(object.expires) < time.Minute {
				_ = l.downloadURL(ctx, object)
			}
			if object.key != "" {
				keys = append(keys, object.key)
			}
		}
		if len(keys) > 0 {
			var released struct {
				Success bool `json:"success"`
			}
			// The backend may report success even when storage deletion failed.
			// Conversely a lost RPC response may hide a successful deletion.
			_ = l.client.rpc(ctx, "cloudAgentNext.releasePendingUploads", map[string]any{"objectKeys": keys}, true, &released)
		}
		allDeleted := true
		for _, object := range l.objects {
			if !object.deleted && object.getURL != "" {
				missing := l.verifyDeleted(ctx, object)
				// Keep ambiguous PUTs eligible for later deletion attempts too:
				// their server-side write could complete after an earlier 404.
				object.deleted = missing && !object.ambiguousPUT
			}
			if !object.deleted || object.ambiguousPUT {
				allDeleted = false
			}
		}
		if allDeleted {
			return nil
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errImageAttachmentCleanup
			case <-timer.C:
			}
		}
	}
	return errImageAttachmentCleanup
}

func (l *imageAttachmentLease) verifyDeleted(ctx context.Context, object *imageAttachmentObject) bool {
	if !time.Now().Before(object.expires) || !validImageAttachmentURL(object.getURL, object.key) {
		return false
	}
	storageURL, _ := url.Parse(object.getURL)
	if object.storageHost == "" || strings.ToLower(storageURL.Host) != object.storageHost || storageURL.Path != object.storagePath {
		return false
	}
	// GET matches the signed operation. Range bounds the response if deletion
	// failed; HEAD cannot reuse an AWS presigned GET signature reliably.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, object.getURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := l.client.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, imageAttachmentResponseLimit))
	// 403 may mean an expired/denied signature while the object still exists.
	return resp.StatusCode == http.StatusNotFound
}
