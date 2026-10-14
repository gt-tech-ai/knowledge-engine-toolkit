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
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	assert.Equal(t, 500, cfg.PageSize)
	assert.Equal(t, 2, cfg.NumConns)
	assert.Equal(t, 9042, cfg.Cassandra.Port)
	assert.Equal(t, 9142, cfg.Keyspaces.Port)

	cluster, err := cassandrabackend.Cluster(&cassandrabackend.Config{
		Hosts:          []string{"c1", "c2"},
		Port:           9042,
		LocalDC:        "dc1",
		Keyspace:       "analytics",
		Consistency:    cfg.Consistency,
		Timeout:        cfg.Timeout,
		ConnectTimeout: cfg.ConnectTimeout,
		PageSize:       cfg.PageSize,
		NumConns:       cfg.NumConns,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"c1", "c2"}, cluster.Hosts)
	assert.Equal(t, gocql.LocalQuorum, cluster.Consistency)
	assert.Equal(t, "analytics", cluster.Keyspace)
	assert.Equal(t, 500, cluster.PageSize)
	assert.NotNil(t, cluster.PoolConfig.HostSelectionPolicy)
	// gocql exports neither policy type, so its dynamic type name is the observable.
	assert.Equal(t, "*gocql.tokenAwareHostPolicy",
		fmt.Sprintf("%T", cluster.PoolConfig.HostSelectionPolicy),
		"a discovering cluster routes token-aware")
	assert.Nil(t, cluster.Authenticator)
	assert.False(t, cluster.DisableInitialHostLookup)

	proxied, err := cassandrabackend.Cluster(&cassandrabackend.Config{
		Hosts:                []string{"127.0.0.1"},
		Port:                 19042,
		Consistency:          cfg.Consistency,
		DisableHostDiscovery: true,
		ReconnectInterval:    2 * time.Second,
	})
	require.NoError(t, err)
	assert.True(t, proxied.DisableInitialHostLookup)
	assert.Equal(t, 2*time.Second, proxied.ReconnectInterval)
	require.NotNil(t, proxied.AddressTranslator)
	ip, port := proxied.AddressTranslator.Translate(net.ParseIP("172.17.0.4"), 9042)
	assert.Equal(t, "127.0.0.1", ip.String())
	assert.Equal(t, 19042, port)
	assert.Equal(t, "*gocql.dcAwareRR",
		fmt.Sprintf("%T", proxied.PoolConfig.HostSelectionPolicy),
		"a pinned cluster routes DC-aware round robin, not token-aware")

	for _, hosts := range [][]string{{"localhost"}, {"10.0.0.1", "10.0.0.2"}} {
		_, err = cassandrabackend.Cluster(&cassandrabackend.Config{
			Hosts:                hosts,
			Consistency:          cfg.Consistency,
			DisableHostDiscovery: true,
		})
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), hosts)
	}

	_, err = cassandrabackend.Cluster(
		&cassandrabackend.Config{Hosts: []string{"c1"}, Consistency: "MOSTLY"},
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
	cluster, err := keyspaces.Cluster(&keyspaces.Config{
		Region:         "us-east-1",
		Port:           9142,
		Keyspace:       "analytics",
		Consistency:    "LOCAL_QUORUM",
		Timeout:        5 * time.Second,
		ConnectTimeout: 5 * time.Second,
		PageSize:       500,
		NumConns:       2,
		Credentials:    aws.CredentialsProvider(provider),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"cassandra.us-east-1.amazonaws.com"}, cluster.Hosts)
	assert.Equal(t, 9142, cluster.Port)
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
	assert.Equal(t, "us-east-1", auth.Region)
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
		&keyspaces.Config{Port: 9142, Credentials: aws.CredentialsProvider(provider)},
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
	want := gocql.NewCluster("x")

	plain, err := cassandrabackend.Cluster(
		&cassandrabackend.Config{Hosts: []string{"c1"}, Consistency: "ONE"},
	)
	require.NoError(t, err)
	assert.Equal(t, want.Timeout, plain.Timeout)
	assert.Equal(t, want.ConnectTimeout, plain.ConnectTimeout)

	ks, err := keyspaces.Cluster(
		&keyspaces.Config{Region: "us-east-1", Consistency: "LOCAL_QUORUM"},
	)
	require.NoError(t, err)
	assert.Equal(t, want.Timeout, ks.Timeout)
	assert.Equal(t, want.ConnectTimeout, ks.ConnectTimeout)
	assert.Equal(t, 9142, ks.Port)
	_, ok := ks.Authenticator.(sigv4.AwsAuthenticator)
	assert.True(t, ok)
}
