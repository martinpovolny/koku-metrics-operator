//
// Copyright 2025 Red Hat Inc.
// SPDX-License-Identifier: Apache-2.0
//

package onpremupload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/project-koku/koku-metrics-operator/internal/testutils"
)

func TestMain(m *testing.M) {
	logf.SetLogger(testutils.ZapLogger(true))
	os.Exit(m.Run())
}

// deleteRequestXML mirrors the AWS S3 DeleteObjects request body.
type deleteRequestXML struct {
	XMLName xml.Name `xml:"Delete"`
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

// s3MockHandler is an httptest handler that mimics path-style S3 endpoints.
type s3MockHandler struct {
	t           *testing.T
	bucket      string
	putError    bool
	aclError    bool
	listError   bool
	deleteError bool
	// objects returned by ListObjectsV2 (key → LastModified)
	listObjects []mockS3Object
	// recorded observations
	putKeys     []string
	aclCalls    int
	deletedKeys []string
}

type mockS3Object struct {
	key          string
	lastModified time.Time
}

func (h *s3MockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	query := r.URL.RawQuery

	switch {
	// PutBucketAcl: PUT /bucket?acl
	case r.Method == http.MethodPut && len(parts) == 1 && strings.Contains(query, "acl"):
		h.aclCalls++
		if h.aclError {
			writeS3Error(w, http.StatusInternalServerError, "InternalError", "simulated acl error")
			return
		}
		w.WriteHeader(http.StatusOK)

	// PutObject: PUT /bucket/key
	case r.Method == http.MethodPut && len(parts) == 2:
		h.putKeys = append(h.putKeys, parts[1])
		if h.putError {
			writeS3Error(w, http.StatusInternalServerError, "InternalError", "simulated put error")
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		w.WriteHeader(http.StatusOK)

	// ListObjectsV2: GET /bucket?list-type=2&...
	case r.Method == http.MethodGet && strings.Contains(query, "list-type=2"):
		if h.listError {
			writeS3Error(w, http.StatusInternalServerError, "InternalError", "simulated list error")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`)
		fmt.Fprintf(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
		fmt.Fprintf(w, `<Name>%s</Name><Prefix>uploads/</Prefix><KeyCount>%d</KeyCount>`, h.bucket, len(h.listObjects))
		for _, obj := range h.listObjects {
			fmt.Fprintf(w, `<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>100</Size></Contents>`,
				obj.key, obj.lastModified.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(w, `</ListBucketResult>`)

	// DeleteObjects: POST /bucket?delete
	case r.Method == http.MethodPost && strings.Contains(query, "delete"):
		var req deleteRequestXML
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			h.t.Logf("failed to decode delete request: %v", err)
		}
		for _, obj := range req.Objects {
			h.deletedKeys = append(h.deletedKeys, obj.Key)
		}
		if h.deleteError {
			writeS3Error(w, http.StatusInternalServerError, "InternalError", "simulated delete error")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>`)
		fmt.Fprintf(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)

	default:
		h.t.Logf("unhandled S3 request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeS3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message><RequestId>test</RequestId></Error>`, code, msg)
}

func s3ConfigFromServer(ts *httptest.Server, bucket string, maxPayloads int) S3Config {
	u, _ := url.Parse(ts.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)
	return S3Config{
		Endpoint:    host,
		Port:        port,
		UseSSL:      false,
		Bucket:      bucket,
		AccessKey:   "testkey",
		SecretKey:   "testsecret",
		MaxPayloads: maxPayloads,
	}
}

// --- BuildIdentity ---

func TestBuildIdentity(t *testing.T) {
	tests := []struct {
		name      string
		clusterID string
		orgID     string
		wantOrgID string
	}{
		{
			name:      "orgID falls back to clusterID when empty",
			clusterID: "cluster-abc",
			orgID:     "",
			wantOrgID: "cluster-abc",
		},
		{
			name:      "explicit orgID is used",
			clusterID: "cluster-xyz",
			orgID:     "org-123",
			wantOrgID: "org-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := BuildIdentity(tt.clusterID, tt.orgID)
			if encoded == "" {
				t.Fatal("BuildIdentity returned empty string")
			}

			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatalf("base64 decode failed: %v", err)
			}

			var identity map[string]any
			if err := json.Unmarshal(decoded, &identity); err != nil {
				t.Fatalf("json unmarshal failed: %v", err)
			}

			if got := identity["org_id"]; got != tt.wantOrgID {
				t.Errorf("org_id: got %v, want %v", got, tt.wantOrgID)
			}

			inner, ok := identity["identity"].(map[string]any)
			if !ok {
				t.Fatal("identity field missing or wrong type")
			}
			if got := inner["org_id"]; got != tt.wantOrgID {
				t.Errorf("identity.org_id: got %v, want %v", got, tt.wantOrgID)
			}
			if got := inner["type"]; got != "User" {
				t.Errorf("identity.type: got %v, want User", got)
			}

			entitlements, ok := identity["entitlements"].(map[string]any)
			if !ok {
				t.Fatal("entitlements field missing or wrong type")
			}
			cm, ok := entitlements["cost_management"].(map[string]any)
			if !ok {
				t.Fatal("entitlements.cost_management missing or wrong type")
			}
			if got := cm["is_entitled"]; got != true {
				t.Errorf("cost_management.is_entitled: got %v, want true", got)
			}
		})
	}
}

// --- PutFile ---

func TestPutFile(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		handler := &s3MockHandler{t: t, bucket: "testbucket"}
		ts := httptest.NewServer(handler)
		defer ts.Close()

		tmpFile, err := os.CreateTemp(t.TempDir(), "payload-*.tar.gz")
		if err != nil {
			t.Fatalf("create temp file: %v", err)
		}
		if _, err := tmpFile.WriteString("fake tarball content"); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		tmpFile.Close()

		cfg := s3ConfigFromServer(ts, "testbucket", 10)
		uuid := "test-uuid-1234"

		gotURL, err := PutFile(context.Background(), cfg, tmpFile.Name(), uuid)
		if err != nil {
			t.Fatalf("PutFile returned unexpected error: %v", err)
		}

		wantKey := "uploads/" + uuid + ".tar.gz"
		if len(handler.putKeys) != 1 || handler.putKeys[0] != wantKey {
			t.Errorf("server received put keys %v, want [%s]", handler.putKeys, wantKey)
		}

		wantURL := fmt.Sprintf("http://%s:%d/testbucket/%s", cfg.Endpoint, cfg.Port, wantKey)
		if gotURL != wantURL {
			t.Errorf("returned URL %q, want %q", gotURL, wantURL)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		handler := &s3MockHandler{t: t, bucket: "testbucket"}
		ts := httptest.NewServer(handler)
		defer ts.Close()

		cfg := s3ConfigFromServer(ts, "testbucket", 10)
		_, err := PutFile(context.Background(), cfg, "/no/such/file.tar.gz", "uuid-x")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})

	t.Run("s3 server error", func(t *testing.T) {
		handler := &s3MockHandler{t: t, bucket: "testbucket", putError: true}
		ts := httptest.NewServer(handler)
		defer ts.Close()

		tmpFile, err := os.CreateTemp(t.TempDir(), "payload-*.tar.gz")
		if err != nil {
			t.Fatalf("create temp file: %v", err)
		}
		tmpFile.Close()

		cfg := s3ConfigFromServer(ts, "testbucket", 10)
		_, err = PutFile(context.Background(), cfg, tmpFile.Name(), "uuid-err")
		if err == nil {
			t.Fatal("expected error from S3 500, got nil")
		}
	})
}

// --- SetBucketPublicRead ---

func TestSetBucketPublicRead(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		handler := &s3MockHandler{t: t, bucket: "testbucket"}
		ts := httptest.NewServer(handler)
		defer ts.Close()

		cfg := s3ConfigFromServer(ts, "testbucket", 10)
		if err := SetBucketPublicRead(context.Background(), cfg); err != nil {
			t.Fatalf("SetBucketPublicRead returned unexpected error: %v", err)
		}
		if handler.aclCalls != 1 {
			t.Errorf("expected 1 ACL call, got %d", handler.aclCalls)
		}
	})

	t.Run("s3 server error", func(t *testing.T) {
		handler := &s3MockHandler{t: t, bucket: "testbucket", aclError: true}
		ts := httptest.NewServer(handler)
		defer ts.Close()

		cfg := s3ConfigFromServer(ts, "testbucket", 10)
		if err := SetBucketPublicRead(context.Background(), cfg); err == nil {
			t.Fatal("expected error from S3 500, got nil")
		}
	})
}

// --- PruneOldPayloads ---

func TestPruneOldPayloads(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	makeObjects := func(keys ...string) []mockS3Object {
		objs := make([]mockS3Object, len(keys))
		for i, k := range keys {
			objs[i] = mockS3Object{key: k, lastModified: base.Add(time.Duration(i) * time.Hour)}
		}
		return objs
	}

	tests := []struct {
		name             string
		listObjects      []mockS3Object
		maxPayloads      int
		listError        bool
		deleteError      bool
		wantErr          bool
		wantDeleted      int
		wantDeleteCalled bool
	}{
		{
			name:             "zero maxPayloads skips everything",
			listObjects:      makeObjects("uploads/a.tar.gz", "uploads/b.tar.gz"),
			maxPayloads:      0,
			wantErr:          false,
			wantDeleteCalled: false,
		},
		{
			name:             "fewer objects than max — no deletion",
			listObjects:      makeObjects("uploads/a.tar.gz", "uploads/b.tar.gz"),
			maxPayloads:      5,
			wantDeleteCalled: false,
		},
		{
			name:             "exactly at max — no deletion",
			listObjects:      makeObjects("uploads/a.tar.gz", "uploads/b.tar.gz"),
			maxPayloads:      2,
			wantDeleteCalled: false,
		},
		{
			name:             "one over max — oldest deleted",
			listObjects:      makeObjects("uploads/a.tar.gz", "uploads/b.tar.gz", "uploads/c.tar.gz"),
			maxPayloads:      2,
			wantDeleted:      1,
			wantDeleteCalled: true,
		},
		{
			name:             "many over max — oldest ones deleted",
			listObjects:      makeObjects("uploads/1.tar.gz", "uploads/2.tar.gz", "uploads/3.tar.gz", "uploads/4.tar.gz", "uploads/5.tar.gz"),
			maxPayloads:      2,
			wantDeleted:      3,
			wantDeleteCalled: true,
		},
		{
			name:        "list error propagates",
			listObjects: nil,
			maxPayloads: 5,
			listError:   true,
			wantErr:     true,
		},
		{
			name:        "delete error propagates",
			listObjects: makeObjects("uploads/a.tar.gz", "uploads/b.tar.gz", "uploads/c.tar.gz"),
			maxPayloads: 1,
			deleteError: true,
			wantErr:     true,
			// SDK retries on 5xx so delete is attempted (and recorded) before the error bubbles up.
			wantDeleteCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &s3MockHandler{
				t:           t,
				bucket:      "testbucket",
				listObjects: tt.listObjects,
				listError:   tt.listError,
				deleteError: tt.deleteError,
			}
			ts := httptest.NewServer(handler)
			defer ts.Close()

			cfg := s3ConfigFromServer(ts, "testbucket", tt.maxPayloads)
			err := PruneOldPayloads(context.Background(), cfg)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantDeleteCalled && len(handler.deletedKeys) == 0 {
				t.Error("expected delete to be called, but no keys were deleted")
			}
			if !tt.wantDeleteCalled && len(handler.deletedKeys) > 0 {
				t.Errorf("expected no deletion, but deleted keys: %v", handler.deletedKeys)
			}
			if tt.wantDeleted > 0 && len(handler.deletedKeys) != tt.wantDeleted {
				t.Errorf("deleted %d keys, want %d", len(handler.deletedKeys), tt.wantDeleted)
			}
		})
	}
}

func TestPruneOldPayloadsDeletesOldest(t *testing.T) {
	// Verify that the oldest objects (by LastModified) are deleted, not the newest.
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	objects := []mockS3Object{
		{key: "uploads/oldest.tar.gz", lastModified: base},
		{key: "uploads/middle.tar.gz", lastModified: base.Add(time.Hour)},
		{key: "uploads/newest.tar.gz", lastModified: base.Add(2 * time.Hour)},
	}

	handler := &s3MockHandler{t: t, bucket: "b", listObjects: objects}
	ts := httptest.NewServer(handler)
	defer ts.Close()

	cfg := s3ConfigFromServer(ts, "b", 2)
	if err := PruneOldPayloads(context.Background(), cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(handler.deletedKeys) != 1 {
		t.Fatalf("expected 1 deleted key, got %v", handler.deletedKeys)
	}
	if handler.deletedKeys[0] != "uploads/oldest.tar.gz" {
		t.Errorf("deleted %q, want uploads/oldest.tar.gz", handler.deletedKeys[0])
	}
}

// --- Announce ---

func TestAnnounce(t *testing.T) {
	t.Run("unreachable broker returns error", func(t *testing.T) {
		// Use a TCP port guaranteed to refuse connections.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		cfg := KafkaConfig{Bootstrap: "127.0.0.1:1", Topic: "platform.upload.announce"}
		msg := AnnounceMessage{
			RequestID:   "req-1",
			OrgID:       "org-1",
			Category:    "tar",
			URL:         "http://s3/bucket/uploads/req-1.tar.gz",
			B64Identity: BuildIdentity("cluster-1", "org-1"),
		}

		err := Announce(ctx, cfg, msg)
		if err == nil {
			t.Fatal("expected error for unreachable Kafka broker, got nil")
		}
	})

	t.Run("json marshal of message is valid", func(t *testing.T) {
		// Verify the AnnounceMessage struct encodes correctly without starting Kafka.
		msg := AnnounceMessage{
			RequestID:   "req-abc",
			Account:     "account-1",
			OrgID:       "org-1",
			Category:    "tar",
			URL:         "http://s3/bucket/uploads/req-abc.tar.gz",
			B64Identity: BuildIdentity("cluster-1", ""),
		}
		msg.Metadata.Reporter = "cost-mgmt"
		msg.Metadata.StaleTimestamp = "2030-01-01T00:00:00Z"

		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		for _, field := range []string{"request_id", "org_id", "category", "url", "b64_identity", "metadata"} {
			if _, ok := out[field]; !ok {
				t.Errorf("missing field %q in JSON output", field)
			}
		}
		if got := out["category"]; got != "tar" {
			t.Errorf("category: got %v, want tar", got)
		}
	})
}
