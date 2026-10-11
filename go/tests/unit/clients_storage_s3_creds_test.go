package unit_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// TestUseStaticCredentials verifies static credentials are presented ONLY for the
// MinIO/dev backend, never for kind="s3" (which must use the default IRSA chain).
//
// Why this test is important:
//   - staging/prod run kind="s3" but inherit base.yaml's access_key_id="minioadmin"
//     (the storage.s3 creds are not blanked, unlike messaging.sqs). Presenting that
//     stray static key to real AWS S3 fails every request with InvalidAccessKeyId and
//     bypasses IRSA — the observed staging orphaned-file-cleanup / presign / download
//     breakage. Gating on Kind (not merely AccessKeyID != "") is the fix.
//
// What it tests:
//   - kind="s3" with a non-empty (leaked) access key does NOT use static creds;
//     kind="minio" with a key does; kind="minio" with no key does not.
func TestUseStaticCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  infra.S3Config
		want bool
	}{
		{
			name: "s3 with leaked minioadmin key uses IRSA, not static",
			cfg: infra.S3Config{
				Kind:            "s3",
				AccessKeyID:     "minioadmin",
				SecretAccessKey: "minioadmin",
			},
			want: false,
		},
		{
			name: "s3 with empty key uses IRSA",
			cfg:  infra.S3Config{Kind: "s3"},
			want: false,
		},
		{
			name: "minio with key uses static creds",
			cfg: infra.S3Config{
				Kind:            "minio",
				AccessKeyID:     "minioadmin",
				SecretAccessKey: "minioadmin",
			},
			want: true,
		},
		{
			name: "minio with empty key uses default chain",
			cfg:  infra.S3Config{Kind: "minio"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := s3.UseStaticCredentials(tc.cfg); got != tc.want {
				t.Errorf("UseStaticCredentials(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

// TestNewConnectorClientBuilder verifies the shared per-connector S3-client builder both
// connector composition roots use.
//
// Why this test is important:
//   - The two roots previously duplicated this ~30-line closure verbatim; it is now the
//     single source, so a bug here breaks BOTH roots' credential-scoped connector client
//     (the crawl would sign with the wrong creds/region). It also guards that the builder
//     stays constructible offline (no lateral go/clients/connector import, no network on
//     build).
//
// What it tests:
//   - The returned builder produces a non-nil StorageClient for an explicit region, and
//     falls back to the base region when the per-connector region is empty — both
//     constructed offline (the AWS client is lazy).
func TestNewConnectorClientBuilder(t *testing.T) {
	t.Parallel()
	build := s3.NewConnectorClientBuilder(infra.S3Config{
		Kind:     "minio",
		Endpoint: "http://minio:9000",
		Region:   "us-east-1",
	})
	ctx := context.Background()
	creds := aws.AnonymousCredentials{}

	client, err := build(ctx, creds, "eu-west-1")
	if err != nil {
		t.Fatalf("build with explicit region: %v", err)
	}
	if client == nil {
		t.Fatal("build returned a nil client for an explicit region")
	}
	// An empty per-connector region falls back to the base region without error.
	fallback, err := build(ctx, creds, "")
	if err != nil {
		t.Fatalf("build with region fallback: %v", err)
	}
	if fallback == nil {
		t.Fatal("region-fallback build returned a nil client")
	}
}
