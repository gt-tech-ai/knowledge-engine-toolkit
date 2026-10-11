// Package suite provides test suite constructors for unit and integration tests.
//
// It carries the schema-agnostic suites (a gomock UnitSuite plus one suite per
// generic backing service — Redis, MinIO, ElasticMQ, Cassandra). A Postgres suite for a
// consumer's own schema belongs with that consumer.
package suite

import (
	"context"

	"go.uber.org/mock/gomock"

	cassandradb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/cassandra"
	elasticmqdb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/elasticmq"
	miniodb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
	redisdb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite/base"
)

// UnitSuite provides a test suite for unit tests with a gomock controller.
type UnitSuite struct {
	// Ctrl is the gomock controller, refreshed before each (sub)test.
	Ctrl *gomock.Controller
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupTest creates a fresh gomock controller for each parent test.
func (s *UnitSuite) SetupTest() {
	s.Ctrl = gomock.NewController(s.T())
}

// SetupSubTest creates a fresh gomock controller for each sub test.
func (s *UnitSuite) SetupSubTest() {
	s.Ctrl = gomock.NewController(s.T())
}

// TearDownTest finishes the gomock controller after each parent test.
func (s *UnitSuite) TearDownTest() {
	s.Ctrl.Finish()
}

// TearDownSubTest finishes the gomock controller after each sub test.
func (s *UnitSuite) TearDownSubTest() {
	s.Ctrl.Finish()
}

// RedisIntegrationSuite provides a test suite with a real Redis instance.
// The RedisAddr field exposes the host:port for the caller to construct its own
// Redis client; no go-redis dependency lives in this package.
type RedisIntegrationSuite struct {
	// redis is the running Redis testcontainer, terminated in TearDownSuite.
	redis *redisdb.TestRedis
	// RedisAddr is the host:port callers use to build a Redis client.
	RedisAddr string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupSuite creates a Redis container and waits until it accepts connections.
func (s *RedisIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	r, err := redisdb.NewTestRedis(context.Background())
	s.Require().NoError(err, "failed to create test Redis")

	s.redis = r
	s.RedisAddr = r.Addr()
}

// TearDownSuite terminates the Redis container.
func (s *RedisIntegrationSuite) TearDownSuite() {
	if s.redis != nil {
		s.redis.Close(context.Background())
	}
}

// MinIOIntegrationSuite provides a test suite with a real MinIO instance.
//
// The MinIOEndpoint field exposes the S3 API URL for the caller to construct its
// own S3 client; credentials are miniodb.RootUser / miniodb.RootPassword.
type MinIOIntegrationSuite struct {
	// minio is the running MinIO container, terminated in TearDownSuite.
	minio *miniodb.TestMinIO
	// MinIOEndpoint is the S3 API URL callers use to build a client.
	MinIOEndpoint string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupSuite creates a MinIO container and waits until it reports healthy.
func (s *MinIOIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	m, err := miniodb.NewTestMinIO(context.Background())
	s.Require().NoError(err, "failed to create test MinIO")

	s.minio = m
	s.MinIOEndpoint = m.Endpoint()
}

// TearDownSuite terminates the MinIO container.
func (s *MinIOIntegrationSuite) TearDownSuite() {
	if s.minio != nil {
		s.minio.Close(context.Background())
	}
}

// ElasticMQIntegrationSuite provides a test suite with a real ElasticMQ instance.
//
// The ElasticMQEndpoint field exposes the SQS-compatible API URL for the caller to
// construct its own AWS-SDK SQS client; credentials are elasticmqdb.AccessKey /
// elasticmqdb.SecretKey.
type ElasticMQIntegrationSuite struct {
	// elasticmq is the running ElasticMQ container, terminated in TearDownSuite.
	elasticmq *elasticmqdb.TestElasticMQ
	// ElasticMQEndpoint is the SQS API URL callers use to build a client.
	ElasticMQEndpoint string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupSuite creates an ElasticMQ container and waits until it accepts connections.
func (s *ElasticMQIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	e, err := elasticmqdb.NewTestElasticMQ(context.Background())
	s.Require().NoError(err, "failed to create test ElasticMQ")

	s.elasticmq = e
	s.ElasticMQEndpoint = e.Endpoint()
}

// TearDownSuite terminates the ElasticMQ container.
func (s *ElasticMQIntegrationSuite) TearDownSuite() {
	if s.elasticmq != nil {
		s.elasticmq.Close(context.Background())
	}
}

// CassandraIntegrationSuite provides a test suite with a real single-node
// Cassandra 5. CassandraHost/CassandraPort let the caller build its own session;
// Cassandra.Exec runs schema bootstrap through cqlsh.
type CassandraIntegrationSuite struct {
	// Cassandra is the running Cassandra container, terminated in TearDownSuite.
	Cassandra *cassandradb.TestCassandra
	// CassandraHost is the host callers connect to.
	CassandraHost string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
	// CassandraPort is the mapped native-protocol port.
	CassandraPort int
}

// SetupSuite starts Cassandra and waits until cqlsh answers.
func (s *CassandraIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	c, err := cassandradb.NewTestCassandra(context.Background())
	s.Require().NoError(err, "failed to create test Cassandra")

	s.Cassandra = c
	s.CassandraHost = c.Host()
	s.CassandraPort = c.Port()
}

// TearDownSuite terminates the Cassandra container.
func (s *CassandraIntegrationSuite) TearDownSuite() {
	if s.Cassandra != nil {
		s.Cassandra.Close(context.Background())
	}
}
