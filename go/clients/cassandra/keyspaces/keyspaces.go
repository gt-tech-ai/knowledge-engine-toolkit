// Package keyspaces builds the gocql cluster configuration for Amazon Keyspaces:
// the regional endpoint on port 9142, mandatory TLS with host verification, SigV4
// authentication (the AWS SigV4 gocql plugin, credentials from an AWS credentials
// provider — the default chain, IRSA in-cluster) and DisableInitialHostLookup, per
// AWS guidance. It never dials; the parent package creates the session.
package keyspaces

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sigv4-auth-cassandra-gocql-driver-plugin/sigv4"
	"github.com/gocql/gocql"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// credentialsTimeout bounds one credentials refresh (an IRSA token exchange).
const credentialsTimeout = 5 * time.Second

// DefaultPort is the Keyspaces TLS native-protocol port.
const DefaultPort = 9142

// Config is the Amazon Keyspaces connection configuration.
type Config struct {
	// Credentials supplies SigV4 credentials; nil loads the AWS default chain on
	// the first credentials refresh, not at construction.
	Credentials aws.CredentialsProvider
	// Region is the AWS region of the Keyspaces endpoint (required).
	Region string
	// Keyspace is the keyspace sessions use.
	Keyspace string
	// Consistency is the default consistency level name (LOCAL_QUORUM for Keyspaces).
	Consistency string
	// CACertPath, when set, is a PEM CA bundle to trust instead of the system pool.
	CACertPath string
	// Timeout bounds each request.
	Timeout time.Duration
	// ConnectTimeout bounds each connection attempt.
	ConnectTimeout time.Duration
	// Port is the TLS native-protocol port (0 = DefaultPort).
	Port int
	// PageSize is the default page size of a query.
	PageSize int
	// NumConns is the number of connections per host.
	NumConns int
}

// Endpoint returns the Keyspaces service endpoint host for region.
func Endpoint(region string) string {
	return "cassandra." + region + ".amazonaws.com"
}

// New returns the cluster configuration for cfg, without dialing or loading
// credentials; a zero port is DefaultPort and a zero timeout keeps the driver's
// default. A missing region or an unknown consistency is CodeInvalidInput.
func New(cfg *Config) (*gocql.ClusterConfig, error) {
	if cfg.Region == "" {
		return nil, apperr.New(apperr.CodeInvalidInput, "keyspaces: region is required")
	}
	consistency, err := gocql.ParseConsistencyWrapper(cfg.Consistency)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"keyspaces: unknown consistency "+cfg.Consistency,
		)
	}
	provider := cfg.Credentials
	if provider == nil {
		provider = defaultChain(cfg.Region)
	}
	host := Endpoint(cfg.Region)
	cluster := gocql.NewCluster(host)
	cluster.Port = DefaultPort
	if cfg.Port > 0 {
		cluster.Port = cfg.Port
	}
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = consistency
	if cfg.Timeout > 0 {
		cluster.Timeout = cfg.Timeout
	}
	if cfg.ConnectTimeout > 0 {
		cluster.ConnectTimeout = cfg.ConnectTimeout
	}
	cluster.PageSize = cfg.PageSize
	if cfg.NumConns > 0 {
		cluster.NumConns = cfg.NumConns
	}
	cluster.DisableInitialHostLookup = true
	cluster.SslOpts = &gocql.SslOptions{
		Config: &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		},
		CaPath:                 cfg.CACertPath,
		EnableHostVerification: true,
	}
	cluster.Authenticator = sigv4.NewAwsAuthenticatorWithCredentialCallback(
		cfg.Region,
		credentialsCallback(provider),
	)
	return cluster, nil
}

// credentialsCallback adapts an AWS SDK v2 credentials provider to the SigV4
// plugin's callback, bounding each refresh.
func credentialsCallback(
	provider aws.CredentialsProvider,
) sigv4.SigV4CredentialsCallback {
	return func() (sigv4.SigV4Credentials, error) {
		ctx, cancel := context.WithTimeout(context.Background(), credentialsTimeout)
		defer cancel()
		creds, err := provider.Retrieve(ctx)
		if err != nil {
			return sigv4.SigV4Credentials{}, apperr.Wrap(
				err,
				apperr.CodeUnavailable,
				"keyspaces: retrieve AWS credentials",
			)
		}
		return sigv4.SigV4Credentials{
			AccessKeyId:     creds.AccessKeyID,
			SecretAccessKey: creds.SecretAccessKey,
			SessionToken:    creds.SessionToken,
		}, nil
	}
}

// defaultChain returns a provider that loads the AWS default credentials chain for
// region on its first Retrieve (so construction does no I/O) and caches it; a
// chain that cannot be loaded is CodeInternal on every Retrieve.
func defaultChain(region string) aws.CredentialsProvider {
	var once sync.Once
	var chain aws.CredentialsProvider
	var loadErr error
	return aws.CredentialsProviderFunc(
		func(ctx context.Context) (aws.Credentials, error) {
			once.Do(func() {
				awsCfg, err := awsconfig.LoadDefaultConfig(
					ctx,
					awsconfig.WithRegion(region),
				)
				if err != nil {
					loadErr = apperr.Wrap(
						err,
						apperr.CodeInternal,
						"keyspaces: load AWS credentials chain",
					)
					return
				}
				chain = awsCfg.Credentials
			})
			if loadErr != nil {
				return aws.Credentials{}, loadErr
			}
			return chain.Retrieve(ctx)
		},
	)
}
