//
// Copyright 2025 Red Hat Inc.
// SPDX-License-Identifier: Apache-2.0
//

package onpremupload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/segmentio/kafka-go"
	logr "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logr.Log.WithName("onpremupload")

const keyPrefix = "uploads/"

// S3Config holds connection details and credentials for the S3 backend.
type S3Config struct {
	Endpoint    string
	Port        int
	UseSSL      bool
	Bucket      string
	AccessKey   string
	SecretKey   string
	MaxPayloads int
}

// KafkaConfig holds connection details for the Kafka broker.
type KafkaConfig struct {
	Bootstrap string
	Topic     string
}

// AnnounceMessage is the payload published to platform.upload.announce.
type AnnounceMessage struct {
	RequestID   string `json:"request_id"`
	Account     string `json:"account"`
	OrgID       string `json:"org_id"`
	Category    string `json:"category"`
	URL         string `json:"url"`
	B64Identity string `json:"b64_identity"`
	Metadata    struct {
		Reporter       string `json:"reporter"`
		StaleTimestamp string `json:"stale_timestamp"`
	} `json:"metadata"`
}

func s3Client(cfg S3Config) *s3.Client {
	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	endpoint := fmt.Sprintf("%s://%s:%d", scheme, cfg.Endpoint, cfg.Port)

	return s3.New(s3.Options{
		BaseEndpoint:       aws.String(endpoint),
		Credentials:        credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		Region:             "onprem",
		UsePathStyle:       true,
		HTTPClient:         &http.Client{},
		EndpointResolverV2: nil,
	})
}

// PutFile uploads the file at localPath to S3 under key uploads/<uuid>.tar.gz
// and returns the HTTP URL koku can GET to retrieve it.
func PutFile(ctx context.Context, cfg S3Config, localPath, uuid string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	key := keyPrefix + uuid + ".tar.gz"
	client := s3Client(cfg)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return "", fmt.Errorf("PutObject %s: %w", key, err)
	}

	scheme := "http"
	if cfg.UseSSL {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d/%s/%s", scheme, cfg.Endpoint, cfg.Port, cfg.Bucket, key)
	log.Info("uploaded tarball to S3", "key", key, "url", url)
	return url, nil
}

// SetBucketPublicRead sets the bucket ACL to public-read so koku can download
// payloads without S3 credentials. Safe for cluster-internal S3 endpoints.
func SetBucketPublicRead(ctx context.Context, cfg S3Config) error {
	client := s3Client(cfg)
	_, err := client.PutBucketAcl(ctx, &s3.PutBucketAclInput{
		Bucket: aws.String(cfg.Bucket),
		ACL:    types.BucketCannedACLPublicRead,
	})
	if err != nil {
		return fmt.Errorf("PutBucketAcl public-read on %s: %w", cfg.Bucket, err)
	}
	log.Info("set bucket ACL to public-read", "bucket", cfg.Bucket)
	return nil
}

// PruneOldPayloads retains only the cfg.MaxPayloads most recent objects under
// the uploads/ prefix, deleting the rest (oldest by LastModified first).
func PruneOldPayloads(ctx context.Context, cfg S3Config) error {
	if cfg.MaxPayloads <= 0 {
		return nil
	}
	client := s3Client(cfg)

	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(cfg.Bucket),
		Prefix: aws.String(keyPrefix),
	})
	if err != nil {
		return fmt.Errorf("ListObjectsV2: %w", err)
	}

	objects := out.Contents
	if len(objects) <= cfg.MaxPayloads {
		return nil
	}

	sort.Slice(objects, func(i, j int) bool {
		return objects[i].LastModified.Before(*objects[j].LastModified)
	})

	toDelete := objects[:len(objects)-cfg.MaxPayloads]
	identifiers := make([]types.ObjectIdentifier, len(toDelete))
	for i, obj := range toDelete {
		identifiers[i] = types.ObjectIdentifier{Key: obj.Key}
	}

	_, err = client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(cfg.Bucket),
		Delete: &types.Delete{Objects: identifiers},
	})
	if err != nil {
		return fmt.Errorf("DeleteObjects: %w", err)
	}

	log.Info("pruned old payloads", "deleted", len(toDelete), "retained", cfg.MaxPayloads)
	return nil
}

// BuildIdentity constructs the base64-encoded x-rh-identity JSON that koku
// expects in the platform.upload.announce Kafka message.
func BuildIdentity(clusterID, orgID string) string {
	if orgID == "" {
		orgID = clusterID
	}
	identity := map[string]any{
		"org_id": orgID,
		"identity": map[string]any{
			"org_id":         orgID,
			"account_number": orgID,
			"type":           "User",
			"user": map[string]any{
				"username":     "metrics-operator",
				"email":        "metrics-operator@cluster.local",
				"is_org_admin": true,
			},
		},
		"entitlements": map[string]any{
			"cost_management": map[string]any{"is_entitled": true},
		},
	}
	b, _ := json.Marshal(identity)
	return base64.StdEncoding.EncodeToString(b)
}

// Announce publishes an AnnounceMessage to the platform.upload.announce topic.
// The "service" header is set to "hccm" as required by koku's message router.
func Announce(ctx context.Context, cfg KafkaConfig, msg AnnounceMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal announce message: %w", err)
	}

	w := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Bootstrap),
		Topic:                  cfg.Topic,
		AllowAutoTopicCreation: false,
	}
	defer w.Close()

	err = w.WriteMessages(ctx, kafka.Message{
		Value: payload,
		Headers: []kafka.Header{
			{Key: "service", Value: []byte("hccm")},
		},
	})
	if err != nil {
		return fmt.Errorf("kafka WriteMessages: %w", err)
	}

	log.Info("published upload announcement", "topic", cfg.Topic, "request_id", msg.RequestID)
	return nil
}
