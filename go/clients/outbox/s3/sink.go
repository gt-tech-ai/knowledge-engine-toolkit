// Package s3 is the S3 OutboxSink: it writes each record as one object, keyed by
// a template, with a SHA-256 integrity checksum (Object Lock buckets require an
// integrity checksum and accept it in place of Content-MD5) and SSE-KMS when
// the record names a key.
package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// DefaultKeyTemplate partitions objects by tenant and UTC creation date.
const DefaultKeyTemplate = "{tenant}/{yyyy}/{mm}/{dd}/{id}"

// KMSKeyAttribute is the record attribute naming the KMS key to encrypt with.
const KMSKeyAttribute = "kms_key_id"

// API is the part of the AWS S3 client the sink uses; *awss3.Client satisfies it.
//
// SDK seam — the AWS S3 SDK client (aws-sdk-go-v2/service/s3), cannot compose with a core
// port.
type API interface {
	// PutObject writes one object.
	PutObject(
		ctx context.Context,
		in *awss3.PutObjectInput,
		optFns ...func(*awss3.Options),
	) (*awss3.PutObjectOutput, error)
}

// Config configures the S3 sink.
type Config struct {
	// API is the S3 client (e.g. *awss3.Client); required. It is injected by
	// the composition root, never read from config.
	API API `yaml:"-" mapstructure:"-"`
	// Bucket is the destination bucket; required.
	Bucket string `yaml:"bucket" mapstructure:"bucket"`
	// KeyTemplate renders each object key from {tenant}, {key} (the record's
	// Key), {yyyy}, {mm}, {dd} (the record's UTC creation date) and {id}; it must
	// contain {id} so keys are unique. Empty uses DefaultKeyTemplate.
	KeyTemplate string `yaml:"key_template" mapstructure:"key_template"`
}

// Sink writes outbox records to one bucket, one object per record.
type Sink struct {
	// api is the (decorated) S3 client.
	api API
	// bucket is the destination bucket.
	bucket string
	// template renders object keys.
	template string
}

// New builds the sink; a missing API or bucket, or a key template without {id},
// is CodeInvalidInput.
func New(cfg Config) (*Sink, error) {
	tmpl := cfg.KeyTemplate
	if tmpl == "" {
		tmpl = DefaultKeyTemplate
	}
	switch {
	case cfg.API == nil || cfg.Bucket == "":
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"outbox s3 sink: api and bucket are required",
		)
	case !strings.Contains(tmpl, "{id}"):
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"outbox s3 sink: key template must contain {id}",
		)
	}
	return &Sink{api: cfg.API, bucket: cfg.Bucket, template: tmpl}, nil
}

// Send writes each record as one object and returns one result per record.
func (s *Sink) Send(ctx context.Context, recs []types.OutboxRecord) []error {
	results := make([]error, len(recs))
	for i := range recs {
		rec := &recs[i]
		if _, err := s.api.PutObject(ctx, s.input(rec)); err != nil {
			results[i] = apperr.Wrap(
				err,
				apperr.CodeOr(err, apperr.CodeUnavailable),
				"outbox s3 sink: put "+rec.ID.String(),
			)
		}
	}
	return results
}

// input builds the PutObject request for rec.
func (s *Sink) input(rec *types.OutboxRecord) *awss3.PutObjectInput {
	sum := sha256.Sum256(rec.Payload)
	in := &awss3.PutObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(s.key(rec)),
		Body:              bytes.NewReader(rec.Payload),
		ChecksumAlgorithm: s3types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	}
	if kms := rec.Attributes[KMSKeyAttribute]; kms != "" {
		in.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		in.SSEKMSKeyId = aws.String(kms)
	}
	return in
}

// key renders the object key for rec.
func (s *Sink) key(rec *types.OutboxRecord) string {
	created := rec.CreatedAt.UTC()
	return strings.NewReplacer(
		"{tenant}", rec.Tenant,
		"{key}", rec.Key,
		"{yyyy}", created.Format("2006"),
		"{mm}", created.Format("01"),
		"{dd}", created.Format("02"),
		"{id}", rec.ID.String(),
	).Replace(s.template)
}
