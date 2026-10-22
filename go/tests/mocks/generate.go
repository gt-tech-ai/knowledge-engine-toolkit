// Package mocks holds generated mocks (mockgen). Regenerate with:
//   go generate ./go/tests/mocks/...
//
// The mock_*.go files are committed. CI regenerates them before it builds and
// tests but does not check them for drift, so regenerate and commit them whenever
// a mocked interface changes; local runs use the committed files.
//go:generate mockgen -destination=mock_analytics.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces AnalyticsStore,AnalyticsCompactor,RowStream
//go:generate mockgen -destination=mock_aws_bedrock.go -package=mocks -mock_names=API=MockAWSBedrockAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/bedrock API
//go:generate mockgen -destination=mock_aws_iam.go -package=mocks -mock_names=API=MockAWSIAMAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/iam API
//go:generate mockgen -destination=mock_aws_route53.go -package=mocks -mock_names=API=MockAWSRoute53API github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/route53 API
//go:generate mockgen -destination=mock_aws_secretsmanager.go -package=mocks -mock_names=API=MockAWSSecretsManagerAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/secretsmanager API
//go:generate mockgen -destination=mock_aws_servicequotas.go -package=mocks -mock_names=API=MockAWSServiceQuotasAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/servicequotas API
//go:generate mockgen -destination=mock_aws_ses.go -package=mocks -mock_names=API=MockAWSSESAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/ses API
//go:generate mockgen -destination=mock_aws_sts.go -package=mocks -mock_names=API=MockAWSSTSAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts API
//go:generate mockgen -destination=mock_bulkhead.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Bulkhead,KeyedLimiter
//go:generate mockgen -destination=mock_cache.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces ByteCache
//go:generate mockgen -destination=mock_cache_invalidator.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces CacheInvalidator
//go:generate mockgen -destination=mock_net.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces HTTPDoer,NSResolver
//go:generate mockgen -destination=mock_fault_injector.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces FaultInjector
//go:generate mockgen -destination=mock_codebase.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Codebase,GoView,YAMLView,CodebaseSource,Renderer
//go:generate mockgen -destination=mock_crypto.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Encryptor
//go:generate mockgen -destination=mock_cql.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces CQLSession,CQLQuery,CQLIter,CQLBatch
//go:generate mockgen -destination=mock_circuit_breaker.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces CircuitBreaker
//go:generate mockgen -destination=mock_client_stream.go -package=mocks google.golang.org/grpc ClientStream
//go:generate mockgen -destination=mock_command_runner.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces CommandRunner,CmdHook
//go:generate mockgen -destination=mock_config_loader.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces ConfigLoader
//go:generate mockgen -destination=mock_connector.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces ConnectorSource,SourceBuilder
//go:generate mockgen -destination=mock_deadletter.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces DeadLetterBackend
//go:generate mockgen -destination=mock_dedup.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Deduplicator
//go:generate mockgen -destination=mock_docker.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces DockerClient
//go:generate mockgen -destination=mock_event.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/events Event
//go:generate mockgen -destination=mock_events.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces EventPublisher
//go:generate mockgen -destination=mock_exec_job.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces AnyJob
//go:generate mockgen -destination=mock_execution.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces ToolSpec
//go:generate mockgen -destination=mock_generator.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Generator
//go:generate mockgen -destination=mock_git_client.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces GitClient
//go:generate mockgen -destination=mock_git_ops.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces GitOps
//go:generate mockgen -destination=mock_helm.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces HelmClient
//go:generate mockgen -destination=mock_kubectl.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces KubectlClient
//go:generate mockgen -destination=mock_leader.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces LeaderElector
//go:generate mockgen -destination=mock_lifecycle.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Lifecycle
//go:generate mockgen -destination=mock_lock.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces DistributedLock,Locker
//go:generate mockgen -destination=mock_logger.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Logger
//go:generate mockgen -destination=mock_messaging.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces MessagePublisher,CountingPublisher,MessageConsumer,DynamicConsumer,PatternConsumer
//go:generate mockgen -destination=mock_metrics.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Metrics,Counter,Histogram,Gauge

// MetricsQuerier — the PromQL query seam a consumer (a steady-state check, the AI-ops
// console) depends on, mocked in place of a Prometheus server.
//go:generate mockgen -destination=mock_metrics_querier.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces MetricsQuerier
//go:generate mockgen -destination=mock_observer.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces ExecutionObserver
//go:generate mockgen -destination=mock_ops.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Operation,Reservation
//go:generate mockgen -destination=mock_outbox.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces OutboxStore,OutboxSink
//go:generate mockgen -destination=mock_outbox_s3_api.go -package=mocks -mock_names=API=MockOutboxS3API github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3 API
//go:generate mockgen -destination=mock_outbox_sqs_api.go -package=mocks -mock_names=API=MockOutboxSQSAPI github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs API
//go:generate mockgen -destination=mock_pipeline.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Pipeline
//go:generate mockgen -destination=mock_probe.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Probe,ProbeSet
//go:generate mockgen -destination=mock_process.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Supervisor,ProcessHandle
//go:generate mockgen -destination=mock_prometheus_http_doer.go -package=mocks -mock_names=HTTPDoer=MockPrometheusHTTPDoer github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus/http HTTPDoer
//go:generate mockgen -destination=mock_prompter.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Prompter
//go:generate mockgen -destination=mock_rate_limiter.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces RateLimiter
//go:generate mockgen -destination=mock_reader.go -package=mocks io ReadCloser
//go:generate mockgen -destination=mock_replaybuffer.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces ReplayBuffer
//go:generate mockgen -destination=mock_repository.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Repository
//go:generate mockgen -destination=mock_roles.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Reader,Lister,Creator,Updater,Writer,Inserter,Upserter,Purger,Deleter,Exister,Suggester,Resource,ReadListExistStore,CRUDNoListStore,CRUDNoListRepository,InsertOnlyStore
//go:generate mockgen -destination=mock_retrier.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Retrier
//go:generate mockgen -destination=mock_round_tripper.go -package=mocks net/http RoundTripper
//go:generate mockgen -destination=mock_s3_api.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3 S3API
//go:generate mockgen -destination=mock_secretsmanager_api.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/awssm SecretsManagerAPI
//go:generate mockgen -destination=mock_secret_source.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Source
//go:generate mockgen -destination=mock_service.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Service
//go:generate mockgen -destination=mock_sqs_api.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs API
//go:generate mockgen -destination=mock_storage.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces StorageClient
//go:generate mockgen -destination=mock_store.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Store
//go:generate mockgen -destination=mock_stream.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Stream
//go:generate mockgen -destination=mock_stream_conn.go -package=mocks connectrpc.com/connect StreamingHandlerConn
//go:generate mockgen -destination=mock_terraform.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces TerraformClient
//go:generate mockgen -destination=mock_toolchain.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain Downloader,PathResolver
//go:generate mockgen -destination=mock_toolrun.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces StepCache,ToolRunner,TargetSelector,Parser
//go:generate mockgen -destination=mock_tracer.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Tracer,Span
//go:generate mockgen -destination=mock_workflow.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Workflow
//go:generate mockgen -destination=mock_sts_assume_role.go -package=mocks github.com/aws/aws-sdk-go-v2/credentials/stscreds AssumeRoleAPIClient
//go:generate mockgen -destination=mock_transaction_manager.go -package=mocks github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces Transaction,TransactionManager

package mocks
