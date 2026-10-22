package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// ConnectorClientBuilder builds a connector-scoped StorageClient signing with a
// connector's OWN credentials + region. It is an UNNAMED func type (an alias, not a
// defined type) so a value of it is assignable to connector/s3.S3ClientBuilder at the
// composition root — this package therefore never imports go/clients/connector, so there
// is no lateral same-layer clients import.
type ConnectorClientBuilder = func(
	ctx context.Context,
	creds aws.CredentialsProvider,
	region string,
) (interfaces.StorageClient, error)

// NewConnectorClientBuilder returns the per-connector S3-client builder a consumer's
// composition roots share, instead of each hand-rolling the client construction. Given
// the app's base S3Config (region + optional MinIO endpoint, captured once), it builds a
// connector-scoped StorageClient signing with the connector's credentials, falling back
// to the app region when the connector carries none. It depends only on storage config,
// so it stays free of any go/clients/connector import; the composition root passes the
// result to connector.NewSourceBuilder.
func NewConnectorClientBuilder(base infra.S3Config) ConnectorClientBuilder {
	endpoint := ""
	if base.Kind == kindMinIO {
		endpoint = base.Endpoint
	}
	return func(
		ctx context.Context,
		creds aws.CredentialsProvider,
		region string,
	) (interfaces.StorageClient, error) {
		if region == "" {
			region = base.Region // per-connector region absent → the app-config region
		}
		cfg := infra.S3Config{Kind: "s3", Region: region}
		if endpoint != "" {
			cfg.Kind = kindMinIO
			cfg.Endpoint = endpoint
			cfg.ForcePathStyle = true
		}
		client, err := NewAWSClientWithCredentials(ctx, cfg, creds, region)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInternal,
				"connector s3: build client",
			)
		}
		return NewClient(client, "", ""), nil
	}
}
