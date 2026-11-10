package unit_test

// Values the clients tests share.
const (
	// text0 is the literal "0".
	text0 = "0"

	// text2m0s is the literal "2m0s".
	text2m0s = "2m0s"

	// text3600 is the literal "3600".
	text3600 = "3600"

	// textAGo is the literal "a.go".
	textAGo = "a.go"

	// textAMd is the literal "a.md".
	textAMd = "a.md"

	// textAYaml is the literal "a.yaml".
	textAYaml = "a.yaml"

	// textAccessKey is the literal "access_key".
	textAccessKey = "access_key"

	// textAccessdenied is the literal "AccessDenied".
	textAccessdenied = "AccessDenied"

	// textAcme is the literal "acme".
	textAcme = "acme"

	// textAll is the literal "all".
	textAll = "all"

	// textAnalytics is the literal "analytics".
	textAnalytics = "analytics"

	// textB is the literal "b".
	textB = "b"

	// textBin is the literal "bin".
	textBin = "bin"

	// textBody is the literal "body".
	textBody = "body"

	// textBogus is the literal "bogus".
	textBogus = "bogus"

	// textBootstrap is the literal "bootstrap".
	textBootstrap = "bootstrap"

	// textBucket is the literal "bucket".
	textBucket = "bucket"

	// textBuf is the literal "buf".
	textBuf = "buf"

	// textBuild is the literal "build".
	textBuild = "build"

	// textChartsAPI is the literal "charts/api".
	textChartsAPI = "charts/api"

	// textClientSecret is the literal "client_secret".
	textClientSecret = "client_secret"

	// textCompose is the literal "compose".
	textCompose = "compose"

	// textConfig is the literal "config".
	textConfig = "config"

	// textCreate is the literal "create".
	textCreate = "create"

	// textCsv is the literal "CSV".
	textCsv = "CSV"

	// textDarwinArm64 is the literal "darwin-arm64".
	textDarwinArm64 = "darwin-arm64"

	// textDelete is the literal "delete".
	textDelete = "delete"

	// textDeploy is the literal "deploy".
	textDeploy = "deploy"

	// textDeployment is the literal "Deployment".
	textDeployment = "Deployment"

	// textDev is the literal "dev".
	textDev = "dev"

	// textDoc1 is the literal "doc-1".
	textDoc1 = "doc-1"

	// textDocker is the literal "docker".
	textDocker = "docker"

	// textDocs is the literal "docs/".
	textDocs = "docs/"

	// textDocsA is the literal "docs/a".
	textDocsA = "docs/a"

	// textDocsB is the literal "docs/b".
	textDocsB = "docs/b"

	// textDown is the literal "down".
	textDown = "down"

	// textFull is the literal "full".
	textFull = "full"

	// textGcc is the literal "gcc".
	textGcc = "gcc"

	// textGenaiCalls is the literal "genai_calls".
	textGenaiCalls = "genai_calls"

	// textGenaiCallsDay is the literal "genai_calls_day".
	textGenaiCallsDay = "genai_calls_day"

	// textGet is the literal "get".
	textGet = "get"

	// textGetTitle is the literal "Get".
	textGetTitle = "Get"

	// textGo is the literal "*.go".
	textGo = "*.go"

	// textGoLower is the literal "go".
	textGoLower = "go"

	// textGobin is the literal "GOBIN".
	textGobin = "GOBIN"

	// textGolangciLint is the literal "golangci-lint".
	textGolangciLint = "golangci-lint"

	// textGotV is the literal "got %v".
	textGotV = "got %v"

	// textGotoolchainLocal is the literal "GOTOOLCHAIN=local".
	textGotoolchainLocal = "GOTOOLCHAIN=local"

	// textGrain is the literal "grain".
	textGrain = "grain"

	// textGrpcurl is the literal "grpcurl".
	textGrpcurl = "grpcurl"

	// textGrpcurlV1 is the literal "grpcurl@v1".
	textGrpcurlV1 = "grpcurl@v1"

	// textHTTPMinio9000 is the literal "http://minio:9000".
	textHTTPMinio9000 = "http://minio:9000"

	// textImage is the literal "image".
	textImage = "image"

	// textInstall is the literal "install".
	textInstall = "install"

	// textK is the literal "k".
	textK = "k"

	// textKubectl is the literal "kubectl".
	textKubectl = "kubectl"

	// textLinuxAmd64 is the literal "linux-amd64".
	textLinuxAmd64 = "linux-amd64"

	// textLinuxApt is the literal "linux-apt".
	textLinuxApt = "linux-apt"

	// textLocal is the literal "local".
	textLocal = "local"

	// textM1 is the literal "m1".
	textM1 = "m1"

	// textMain is the literal "main".
	textMain = "main"

	// textMinio is the literal "minio".
	textMinio = "minio"

	// textMinioadmin is the literal "minioadmin".
	textMinioadmin = "minioadmin"

	// textMissing is the literal "missing".
	textMissing = "missing"

	// textMsg1 is the literal "msg-1".
	textMsg1 = "msg-1"

	// textNope is the literal "nope".
	textNope = "nope"

	// textObjKeyTxt is the literal "obj/key.txt".
	textObjKeyTxt = "obj/key.txt"

	// textOnce is the literal "once".
	textOnce = "once"

	// textOne is the literal "one".
	textOne = "one"

	// textOrg1 is the literal "org-1".
	textOrg1 = "org-1"

	// textOrigin is the literal "origin".
	textOrigin = "origin"

	// textP is the literal "p".
	textP = "p"

	// textPath is the literal "PATH".
	textPath = "PATH"

	// textPgcli is the literal "pgcli".
	textPgcli = "pgcli"

	// textPgx is the literal "pgx".
	textPgx = "pgx"

	// textPinned is the literal "pinned".
	textPinned = "pinned"

	// textPip is the literal "pip".
	textPip = "pip"

	// textPlain is the literal "plain".
	textPlain = "plain"

	// textPowershell is the literal "powershell".
	textPowershell = "powershell"

	// textProbe is the literal "probe".
	textProbe = "probe"

	// textPrometheus is the literal "prometheus".
	textPrometheus = "prometheus"

	// textQ is the literal "q".
	textQ = "q"

	// textReports is the literal "reports".
	textReports = "reports"

	// textRetrieval is the literal "retrieval".
	textRetrieval = "retrieval"

	// textRoot is the literal "/root".
	textRoot = "/root"

	// textRuff is the literal "ruff".
	textRuff = "ruff"

	// textRun is the literal "run".
	textRun = "run"

	// textSLower is the literal "%s".
	textSLower = "%s"

	// textSecret is the literal "secret".
	textSecret = "secret"

	// textStg is the literal "stg".
	textStg = "stg"

	// textStore is the literal "store".
	textStore = "store"

	// textT is the literal "t".
	textT = "t"

	// textT1 is the literal "t-1".
	textT1 = "t-1"

	// textTarget is the literal "target".
	textTarget = "target"

	// textTCP is the literal "tcp".
	textTCP = "tcp"

	// textTenant is the literal "tenant".
	textTenant = "tenant"

	// textTerraform is the literal "terraform".
	textTerraform = "terraform"

	// textTextPlain is the literal "text/plain".
	textTextPlain = "text/plain"

	// textTok is the literal "tok".
	textTok = "tok"

	// textTwo is the literal "two".
	textTwo = "two"

	// textU is the literal "u".
	textU = "u"

	// textUsEast1 is the literal "us-east-1".
	textUsEast1 = "us-east-1"

	// textUsrBin is the literal "/usr/bin".
	textUsrBin = "/usr/bin"

	// textUv is the literal "uv".
	textUv = "uv"

	// textV is the literal "v".
	textV = "v"

	// textVLowerAlt is the literal "%v".
	textVLowerAlt = "%v"

	// textVersion is the literal "version".
	textVersion = "version"

	// textVersionFlag is the literal "--version".
	textVersionFlag = "--version"

	// textVpc is the literal "vpc".
	textVpc = "vpc"

	// textWindows is the literal "windows".
	textWindows = "windows"

	// textWire is the literal "wire".
	textWire = "wire"

	// text127001 is the literal "127.0.0.1:0".
	text127001 = "127.0.0.1:0"

	// textO is the literal "o".
	textO = "o"

	// textPkgV1 is the literal "pkg@v1".
	textPkgV1 = "pkg@v1"

	// textPackageA is the literal "package a\n".
	textPackageA = "package a\n"

	// textUp is the literal "up".
	textUp = "up"

	// dateInput is the year passed to Date in
	// TestCassandraStore_WriteUpsertsOnePartialRowPerGrain, TestCursorCodec_EmptyID and
	// 10 more.
	dateInput = 2026

	// drainInput is the limit passed to drain in TestS3Inventory_PagesMatchTheS3Kind,
	// TestS3Inventory_ReadsTheNewestCompleteReportUnderAPrefix and 1 more.
	drainInput = 2

	// fileModeInput is argument 1 of FileMode in TestClientsProcess_LogFile,
	// TestPlanStore_RoundTrip and 3 more.
	fileModeInput = 0o600

	// initialI is the factor multiplying page in serveAuth0Orgs, and the value assigned
	// to i in testServicesProcedureProbeKindsStatuses.
	initialI = 2

	// listPageInput is the limit passed to ListPage in
	// testClientDecorateFamiliesCarryEveryResultReplayBufferFamily and
	// testS3InventoryRefusesMalformedDataFilesCase.
	listPageInput = 10

	// mkdirAllInput is the perm passed to MkdirAll in TestUpsertProfile and writePlan.
	mkdirAllInput = 0o700

	// presignURLInput is the expirySeconds passed to PresignURL in
	// TestStorageClient_Presign_BuildsSignedURLs,
	// TestStorageClient_Presign_EncodesUnsafeFilename and 1 more.
	presignURLInput = 900

	// returnInput is the rets passed to Return in
	// testClientDecorateFamiliesCarryEveryResultReplayBufferFamily and
	// testRepositoryOutboxClaimsOnceAndRetriesTheRest.
	returnInput = 7

	// wantAllLen is the expected all len.
	wantAllLen = 3

	// wantCalls is the expected calls.
	wantCalls = 2

	// wantClientOpCounter is the expected client op counter.
	wantClientOpCounter = 1.0

	// wantGot is the expected got.
	wantGot = 42

	// wantI is the expected i.
	wantI = 2

	// wantLen is the expected len.
	wantLen = 2

	// wantN is the expected n.
	wantN = 7

	// wantObjsLen is the expected objs len.
	wantObjsLen = 2

	// wantPort is the expected port.
	wantPort = 8090

	// wantRuns is the expected runs.
	wantRuns = 3
)
