package unit_test

import (
	"crypto/tls"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sigv4-auth-cassandra-gocql-driver-plugin/sigv4"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	cassandrabackend "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql/keyspaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestDefaultConfig_IsLocalQuorum tests the CQL client defaults and the
// self-managed backend's cluster settings.
//
// Why this test is important:
//   - LOCAL_QUORUM, bounded timeouts and a token-aware, DC-aware policy are what
//     keep reads consistent and local; a silent default change would weaken both
//
// What it tests:
//   - DefaultConfig: kind cassandra, LOCAL_QUORUM, 5 s timeouts, page size 500,
//     2 connections, port 9042 (Keyspaces 9142)
//   - the cassandra backend's cluster carries those values, the hosts and a
//     token-aware host-selection policy, without dialing; host discovery stays on
//   - DisableHostDiscovery on one IP-literal host skips the peer lookup, sets the
//     reconnect interval, translates any advertised address to that host and port,
//     and selects hosts DC-aware round robin rather than
//     token-aware, whose nil-host dereference crashes a pinned cluster's outage; a
//     hostname or several hosts are CodeInvalidInput
//   - an unknown consistency is CodeInvalidInput
func TestDefaultConfig_IsLocalQuorum(t *testing.T) {
	t.Parallel()

	cfg := cql.DefaultConfig()
	assert.Equal(t, cql.KindCassandra, cfg.Kind)
	assert.Equal(t, "cassandra", string(cfg.Kind))
	assert.Equal(t, "LOCAL_QUORUM", cfg.Consistency)
	assert.Equal(t, 5*time.Second, cfg.Timeout)
	assert.Equal(t, 5*time.Second, cfg.ConnectTimeout)
	assert.Equal(t, wantPageSize, cfg.PageSize)
	assert.Equal(t, wantNumConns, cfg.NumConns)
	assert.Equal(t, wantPortQuorum, cfg.Cassandra.Port)
	assert.Equal(t, wantPortLocalQuorum, cfg.Keyspaces.Port)

	cluster, err := cassandrabackend.Cluster(
		fixtures.BuildRef(func(row *cassandrabackend.Config) {
			row.Hosts = []string{"c1", "c2"}
			row.Port = 9042
			row.LocalDC = "dc1"
			row.Keyspace = textAnalytics
			row.Consistency = cfg.Consistency
			row.Timeout = cfg.Timeout
			row.ConnectTimeout = cfg.ConnectTimeout
			row.PageSize = cfg.PageSize
			row.NumConns = cfg.NumConns
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"c1", "c2"}, cluster.Hosts)
	assert.Equal(t, gocql.LocalQuorum, cluster.Consistency)
	assert.Equal(t, textAnalytics, cluster.Keyspace)
	assert.Equal(t, wantPageSize, cluster.PageSize)
	assert.NotNil(t, cluster.PoolConfig.HostSelectionPolicy)
	// gocql exports neither policy type, so its dynamic type name is the observable.
	assert.Equal(t, "*gocql.tokenAwareHostPolicy",
		fmt.Sprintf("%T", cluster.PoolConfig.HostSelectionPolicy),
		"a discovering cluster routes token-aware")
	assert.Nil(t, cluster.Authenticator)

	testDefaultConfigIsLocalQuorumProxiedCluster(t, &cfg, cluster)
}

// testDefaultConfigIsLocalQuorumProxiedCluster continues
// TestDefaultConfig_IsLocalQuorum: proxied cluster.
func testDefaultConfigIsLocalQuorumProxiedCluster(
	t *testing.T,
	cfg *cql.Config,
	cluster *gocql.ClusterConfig,
) {
	t.Helper()

	var err error

	assert.False(t, cluster.DisableInitialHostLookup)

	proxied, err := cassandrabackend.Cluster(
		fixtures.BuildRef(func(row *cassandrabackend.Config) {
			row.Hosts = []string{"127.0.0.1"}
			row.Port = 19042
			row.Consistency = cfg.Consistency
			row.DisableHostDiscovery = true
			row.ReconnectInterval = 2 * time.Second
		}),
	)
	require.NoError(t, err)
	assert.True(t, proxied.DisableInitialHostLookup)
	assert.Equal(t, 2*time.Second, proxied.ReconnectInterval)
	require.NotNil(t, proxied.AddressTranslator)

	ip, port := proxied.AddressTranslator.Translate(
		net.ParseIP("172.17.0.4"),
		translateInput,
	)
	assert.Equal(t, "127.0.0.1", ip.String())
	assert.Equal(t, wantPortCluster, port)
	assert.Equal(t, "*gocql.dcAwareRR",
		fmt.Sprintf("%T", proxied.PoolConfig.HostSelectionPolicy),
		"a pinned cluster routes DC-aware round robin, not token-aware")

	for _, hosts := range [][]string{{"localhost"}, {"10.0.0.1", "10.0.0.2"}} {
		_, err = cassandrabackend.Cluster(
			fixtures.BuildRef(func(row *cassandrabackend.Config) {
				row.Hosts = hosts
				row.Consistency = cfg.Consistency
				row.DisableHostDiscovery = true
			}),
		)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), hosts)
	}

	_, err = cassandrabackend.Cluster(
		fixtures.BuildRef(func(row *cassandrabackend.Config) {
			row.Hosts = []string{"c1"}
			row.Consistency = "MOSTLY"
		}),
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestKeyspacesConfig_UsesTLSAndSigV4 tests the Amazon Keyspaces cluster settings,
// inspected without dialing.
//
// Why this test is important:
//   - Keyspaces rejects plaintext and password auth; a cluster built without TLS,
//     SigV4 or the regional endpoint fails only at deploy time
//
// What it tests:
//   - host cassandra.<region>.amazonaws.com, port 9142, TLS with host verification
//     (TLS ≥ 1.2), a SigV4 authenticator for the region whose credentials come from
//     the injected AWS provider, LOCAL_QUORUM and DisableInitialHostLookup
func TestKeyspacesConfig_UsesTLSAndSigV4(t *testing.T) {
	t.Parallel()

	provider := credentials.NewStaticCredentialsProvider("AKID", "SECRET", "TOKEN")
	cluster, err := keyspaces.Cluster(t.Context(), &keyspaces.Config{
		Region:         textUsEast1,
		Port:           9142,
		Keyspace:       textAnalytics,
		Consistency:    "LOCAL_QUORUM",
		Timeout:        5 * time.Second,
		ConnectTimeout: 5 * time.Second,
		PageSize:       500,
		NumConns:       2,
		Credentials:    aws.CredentialsProvider(provider),
		CACertPath:     "",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"cassandra.us-east-1.amazonaws.com"}, cluster.Hosts)
	assert.Equal(t, wantCQLPort, cluster.Port)
	require.NotNil(t, cluster.SslOpts)
	assert.True(t, cluster.SslOpts.EnableHostVerification)
	assert.Equal(
		t,
		"cassandra.us-east-1.amazonaws.com",
		cluster.SslOpts.ServerName,
	)
	assert.Equal(t, uint16(tls.VersionTLS12), cluster.SslOpts.MinVersion)
	assert.Equal(t, gocql.LocalQuorum, cluster.Consistency)
	assert.True(t, cluster.DisableInitialHostLookup)

	auth, ok := cluster.Authenticator.(sigv4.AwsAuthenticator)
	require.True(t, ok)
	assert.Equal(t, textUsEast1, auth.Region)

	creds, err := auth.CredentialsCallback()
	require.NoError(t, err)
	assert.Equal(
		t,
		sigv4.SigV4Credentials{
			AccessKeyId:     "AKID",
			SecretAccessKey: "SECRET",
			SessionToken:    "TOKEN",
		},
		creds,
	)

	_, err = keyspaces.Cluster(
		t.Context(),
		fixtures.BuildRef(func(row *keyspaces.Config) {
			row.Port = 9142
			row.Credentials = aws.CredentialsProvider(provider)
		}),
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "region is required")
}

// TestClusterConfig_ZeroValuesKeepDriverDefaults tests that omitted timeouts and
// port leave the driver's defaults in place.
//
// Why this test is important:
//   - A YAML overlay that omits a timeout yields a zero duration; copied into the
//     cluster it disables the driver's timeout, so a query to a dead node waits
//     forever and the circuit breaker never trips
//
// What it tests:
//   - for both backends, a config with zero Timeout and ConnectTimeout keeps the
//     timeouts of gocql.NewCluster; keyspaces with zero Port uses 9142
//   - keyspaces without injected credentials builds a SigV4 cluster without
//     loading the AWS chain at construction
func TestClusterConfig_ZeroValuesKeepDriverDefaults(t *testing.T) {
	t.Parallel()

	want := gocql.NewCluster(textX)

	plain, err := cassandrabackend.Cluster(
		fixtures.BuildRef(func(row *cassandrabackend.Config) {
			row.Hosts = []string{"c1"}
			row.Consistency = "ONE"
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, want.Timeout, plain.Timeout)
	assert.Equal(t, want.ConnectTimeout, plain.ConnectTimeout)

	clusterConfig, err := keyspaces.Cluster(
		t.Context(),
		fixtures.BuildRef(func(row *keyspaces.Config) {
			row.Region = textUsEast1
			row.Consistency = "LOCAL_QUORUM"
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, want.Timeout, clusterConfig.Timeout)
	assert.Equal(t, want.ConnectTimeout, clusterConfig.ConnectTimeout)
	assert.Equal(t, wantPortDefaults, clusterConfig.Port)

	_, ok := clusterConfig.Authenticator.(sigv4.AwsAuthenticator)
	assert.True(t, ok)
}

const (
	// translateInput is the translate input the tests use.
	translateInput = 9042

	// wantNumConns is the expected num conns.
	wantNumConns = 2

	// wantPageSize is the expected page size.
	wantPageSize = 500

	// wantPortCluster is the expected port cluster.
	wantPortCluster = 19042

	// wantPortDefaults is the expected port defaults.
	wantPortDefaults = 9142

	// wantPortLocalQuorum is the expected port local quorum.
	wantPortLocalQuorum = 9142

	// wantPortQuorum is the expected port quorum.
	wantPortQuorum = 9042

	// wantCQLPort is the expected port v4.
	wantCQLPort = 9142
)
