package unit_test

// Values the foundation tests share.
const (
	// text0000 is the literal "0.0.0.0".
	text0000 = "0.0.0.0"

	// text00f067aa0ba902b7 is the literal "00f067aa0ba902b7".
	text00f067aa0ba902b7 = "00f067aa0ba902b7"

	// text200 is the literal "200".
	text200 = "200"

	// text400 is the literal "400".
	text400 = "400"

	// text4bf92f3577b34da6a3ce929d0e is the literal "4bf92f3577b34da6a3ce929d0e0e4736".
	text4bf92f3577b34da6a3ce929d0e = "4bf92f3577b34da6a3ce929d0e0e4736"

	// text99 is the literal "99".
	text99 = "99"

	// textA is the literal "a".
	textA = "a"

	// textAB is the literal "A=b".
	textAB = "A=b"

	// textABCom is the literal "a@b.com".
	textABCom = "a@b.com"

	// textAbsent is the literal "absent".
	textAbsent = "absent"

	// textAccessControlAllowOrigin is the literal "Access-Control-Allow-Origin".
	textAccessControlAllowOrigin = "Access-Control-Allow-Origin"

	// textActiveLower is the literal "active".
	textActiveLower = "active"

	// textAdmin is the literal "admin".
	textAdmin = "admin"

	// textAlice is the literal "Alice".
	textAlice = "Alice"

	// textAPI is the literal "api".
	textAPI = "api"

	// textAPIPath is the literal "/api".
	textAPIPath = "/api"

	// textAPIV1Documents is the literal "/api/v1/documents".
	textAPIV1Documents = "/api/v1/documents"

	// textAPIV1DocumentsDocument is the literal "/api/v1/documents/{document_id}".
	textAPIV1DocumentsDocument = "/api/v1/documents/{document_id}"

	// textAPIV1DocumentsDocumentPath is the literal
	// "/api/v1/documents/{document_id}/confirm".
	textAPIV1DocumentsDocumentPath = "/api/v1/documents/{document_id}/confirm"

	// textApp is the literal "app".
	textApp = "app"

	// textBaseYaml is the literal "base.yaml".
	textBaseYaml = "base.yaml"

	// textBoom is the literal "boom".
	textBoom = "boom"

	// textBraceCloseNewline is the literal "}\n".
	textBraceCloseNewline = "}\n"

	// textBroken is the literal "broken".
	textBroken = "broken"

	// textC is the literal "c".
	textC = "c"

	// textContentEncoding is the literal "Content-Encoding".
	textContentEncoding = "Content-Encoding"

	// textCount is the literal "count".
	textCount = "count"

	// textCreatedAt is the literal "created_at".
	textCreatedAt = "created_at"

	// textDBinpb is the literal "d.binpb".
	textDBinpb = "d.binpb"

	// textData is the literal "data".
	textData = "data"

	// textDataPathsYaml is the literal "data/paths.yaml".
	textDataPathsYaml = "data/paths.yaml"

	// textDatabase is the literal "database".
	textDatabase = "database"

	// textDBHost is the literal "DB_HOST".
	textDBHost = "DB_HOST"

	// textDebug is the literal "debug".
	textDebug = "debug"

	// textDeleteUpper is the literal "DELETE".
	textDeleteUpper = "DELETE"

	// textDescription is the literal "description".
	textDescription = "description"

	// textDevPassword is the literal "dev_password".
	textDevPassword = "dev_password"

	// textDisable is the literal "disable".
	textDisable = "disable"

	// textDockerAPIDockerfile is the literal "docker/api/Dockerfile".
	textDockerAPIDockerfile = "docker/api/Dockerfile"

	// textDockerfile is the literal "Dockerfile".
	textDockerfile = "Dockerfile"

	// textDocsLower is the literal "docs".
	textDocsLower = "docs"

	// textDocument is the literal "document".
	textDocument = "document"

	// textDocumentTitle is the literal "Document".
	textDocumentTitle = "Document"

	// textDocuments is the literal "documents".
	textDocuments = "documents"

	// textDotDot is the literal "..".
	textDotDot = ".."

	// textDotSlashDotDotDot is the literal "./...".
	textDotSlashDotDotDot = "./..."

	// textE is the literal "e".
	textE = "e"

	// textEmail is the literal "email".
	textEmail = "email"

	// textEmpty is the literal "empty".
	textEmpty = "empty"

	// textEmptyTitle is the literal "Empty".
	textEmptyTitle = "Empty"

	// textError is the literal "error".
	textError = "error"

	// textEuWest1 is the literal "eu-west-1".
	textEuWest1 = "eu-west-1"

	// textExpectedErrorForUnknown is the literal "expected error for unknown kind".
	textExpectedErrorForUnknown = "expected error for unknown kind"

	// textExpectedNonNilLogger is the literal "expected non-nil logger".
	textExpectedNonNilLogger = "expected non-nil logger"

	// textExpectedNonNilMetrics is the literal "expected non-nil metrics".
	textExpectedNonNilMetrics = "expected non-nil metrics"

	// textFailed is the literal "failed".
	textFailed = "failed"

	// textFirstName is the literal "first_name".
	textFirstName = "first_name"

	// textFooBar is the literal "FOO=bar".
	textFooBar = "FOO=bar"

	// textGen is the literal "gen".
	textGen = "gen"

	// textGetUpper is the literal "GET".
	textGetUpper = "GET"

	// textGetdocument is the literal "GetDocument".
	textGetdocument = "GetDocument"

	// textGoExt is the literal ".go".
	textGoExt = ".go"

	// textGoMod is the literal "go.mod".
	textGoMod = "go.mod"

	// textGoWork is the literal "go.work".
	textGoWork = "go.work"

	// textGofmt is the literal "gofmt".
	textGofmt = "gofmt"

	// textGone is the literal "gone".
	textGone = "gone"

	// textGzip is the literal "gzip".
	textGzip = "gzip"

	// textHelp is the literal "help".
	textHelp = "help"

	// textID is the literal "id".
	textID = "id"

	// textIn is the literal "in".
	textIn = "in"

	// textIndexed is the literal "indexed".
	textIndexed = "indexed"

	// textInfo is the literal "info".
	textInfo = "info"

	// textInt is the literal "int".
	textInt = "int"

	// textJoinedAt is the literal "joined_at".
	textJoinedAt = "joined_at"

	// textStructuredFormat is the literal "json".
	textStructuredFormat = "json"

	// textKey is the literal "key".
	textKey = "key"

	// textKind99 is the literal "Kind(99)".
	textKind99 = "Kind(99)"

	// textLastName is the literal "last_name".
	textLastName = "last_name"

	// textLibs is the literal "libs".
	textLibs = "libs"

	// textList is the literal "list".
	textList = "list"

	// textLocalhost4317 is the literal "localhost:4317".
	textLocalhost4317 = "localhost:4317"

	// textLogging is the literal "logging".
	textLogging = "logging"

	// textMax is the literal "max".
	textMax = "max"

	// textMeta is the literal "meta".
	textMeta = "meta"

	// textMethod is the literal "method".
	textMethod = "method"

	// textModel is the literal "model".
	textModel = "model"

	// textMyapp is the literal "MYAPP".
	textMyapp = "MYAPP"

	// textN is the literal "n".
	textN = "n"

	// textName is the literal "name".
	textName = "name"

	// textNameTitle is the literal "Name".
	textNameTitle = "Name"

	// textNever is the literal "never".
	textNever = "never"

	// textNewline is the literal "\n".
	textNewline = "\n"

	// textNotStructured is the literal "{not json".
	textNotStructured = "{not json"

	// textNull is the literal "null".
	textNull = "null"

	// textOk is the literal "ok".
	textOk = "ok"

	// textOp is the literal "op".
	textOp = "op"

	// textOpen is the literal "(".
	textOpen = "("

	// textOrders is the literal "orders".
	textOrders = "orders"

	// textOther is the literal "other".
	textOther = "other"

	// textOut is the literal "out".
	textOut = "out"

	// textPageCount is the literal "page_count".
	textPageCount = "page_count"

	// textPatch is the literal "PATCH".
	textPatch = "PATCH"

	// textPathLower is the literal "path".
	textPathLower = "path"

	// textPaths is the literal "paths".
	textPaths = "paths"

	// textPost is the literal "POST".
	textPost = "POST"

	// textPreflight is the literal "preflight".
	textPreflight = "preflight"

	// textProject is the literal "[project]\n".
	textProject = "[project]\n"

	// textProps is the literal "props".
	textProps = "props"

	// textProvision is the literal "Provision".
	textProvision = "Provision"

	// textPython is the literal "python".
	textPython = "python"

	// textRead is the literal "read".
	textRead = "read"

	// textRecordNotFound is the literal "record not found".
	textRecordNotFound = "record not found"

	// textRedacted is the literal "[REDACTED]".
	textRedacted = "[REDACTED]"

	// textRedis is the literal "redis".
	textRedis = "redis"

	// textRef is the literal "$ref".
	textRef = "$ref"

	// textRegex is the literal "regex".
	textRegex = "regex"

	// textRep is the literal "rep".
	textRep = "rep"

	// textRepo is the literal "/repo".
	textRepo = "/repo"

	// textRepository is the literal "repository".
	textRepository = "repository"

	// textResolved is the literal "resolved".
	textResolved = "resolved"

	// textRole is the literal "role".
	textRole = "role"

	// textScalar is the literal "scalar".
	textScalar = "scalar"

	// textSecurity is the literal "security".
	textSecurity = "security"

	// textService is the literal "service".
	textService = "service"

	// textServices is the literal "services".
	textServices = "services"

	// textSpaceSpaceSpace is the literal "   ".
	textSpaceSpaceSpace = "   "

	// textStaging is the literal "staging".
	textStaging = "staging"

	// textStatus is the literal "status".
	textStatus = "status"

	// textString is the literal "string".
	textString = "string"

	// textSub is the literal "sub".
	textSub = "sub"

	// textSumInputCount is the literal "sum(tokens_in)".
	textSumInputCount = "sum(tokens_in)"

	// textSvc is the literal "svc".
	textSvc = "svc"

	// textTags is the literal "tags".
	textTags = "tags"

	// textTeam is the literal "team".
	textTeam = "team"

	// textTest is the literal "test".
	textTest = "test"

	// textTestService is the literal "test-service".
	textTestService = "test-service"

	// textTestSwaggerJSON is the literal "test.swagger.json".
	textTestSwaggerJSON = "test.swagger.json"

	// textText is the literal "text".
	textText = "text"

	// textTf is the literal ".tf".
	textTf = ".tf"

	// textTimeout is the literal "timeout".
	textTimeout = "timeout"

	// textTimeoutTitle is the literal "Timeout".
	textTimeoutTitle = "Timeout"

	// textTitle is the literal "title".
	textTitle = "title"

	// textTokensIn is the literal "tokens_in".
	textTokensIn = "tokens_in"

	// textTool is the literal "tool".
	textTool = "tool"

	// textTools is the literal "tools".
	textTools = "tools"

	// textTracing is the literal "tracing".
	textTracing = "tracing"

	// textUnexpectedError is the literal "unexpected error".
	textUnexpectedError = "unexpected error"

	// textUnitTests is the literal "Unit Tests".
	textUnitTests = "Unit Tests"

	// textUnknown is the literal "unknown".
	textUnknown = "unknown"

	// textUnknownField is the literal "unknown field".
	textUnknownField = "unknown field"

	// textUnknownKindShouldContain is the literal shown below.
	textUnknownKindShouldContain = "unknown kind should contain its numeric value"

	// textUnknownOperator is the literal "unknown operator".
	textUnknownOperator = "unknown operator"

	// textUnmarshalkey is the literal "UnmarshalKey".
	textUnmarshalkey = "UnmarshalKey"

	// textURL is the literal "url".
	textURL = "url"

	// textUser is the literal "user".
	textUser = "user"

	// textUserID is the literal "user_id".
	textUserID = "user_id"

	// textUsers is the literal "users".
	textUsers = "users"

	// textV1 is the literal "v1".
	textV1 = "v1"

	// textValue is the literal "value".
	textValue = "value"

	// textVet is the literal "vet".
	textVet = "vet"

	// textVirtualEnv is the literal "-VIRTUAL_ENV".
	textVirtualEnv = "-VIRTUAL_ENV"

	// textWarn is the literal "warn".
	textWarn = "warn"

	// textWorkspace is the literal "workspace".
	textWorkspace = "workspace"

	// textX is the literal "x".
	textX = "x"

	// textXRequestID is the literal "X-Request-ID".
	textXRequestID = "X-Request-ID"

	// textYaml is the literal ".yaml".
	textYaml = ".yaml"

	// text401 is the literal "401".
	text401 = "401"

	// textAppEnv is the literal "APP_ENV".
	textAppEnv = "APP_ENV"

	// textAppName is the literal "app.name".
	textAppName = "app.name"

	// textBCGo is the literal "b/c.go".
	textBCGo = "b/c.go"

	// textDLower is the literal "d".
	textDLower = "d"

	// textLabel is the literal "label".
	textLabel = "label"

	// textOtok is the literal "otok".
	textOtok = "otok"

	// textPending is the literal "pending".
	textPending = "pending"

	// textPyprojectToml is the literal "pyproject.toml".
	textPyprojectToml = "pyproject.toml"

	// textServerPort is the literal "server.port".
	textServerPort = "server.port"

	// textSrc is the literal "src".
	textSrc = "src"

	// textSvcs is the literal "svcs".
	textSvcs = "svcs"

	// textType is the literal "type".
	textType = "type"

	// textVal is the literal "val".
	textVal = "val"

	// text404 is the literal "404".
	text404 = "404"

	// textOld is the literal "old\n".
	textOld = "old\n"

	// addInput is the add input the tests use.
	addInput = 10.0

	// duration is the duration the tests use.
	duration = 2

	// gaugeReading is the gauge reading the tests use.
	gaugeReading = 100.0

	// initialExpiry is the initial expiry the tests use.
	initialExpiry = 60

	// items is the items the tests use.
	items = 2

	// iterations is the iterations the tests use.
	iterations = 2

	// kindInput is the kind input the tests use.
	kindInput = 99

	// kindInputError is the kind input error the tests use.
	kindInputError = 999

	// mkdirAllInputErrors is the mkdir all input errors the tests use.
	mkdirAllInputErrors = 0o750

	// mkdirAllInputWrite is the mkdir all input write the tests use.
	mkdirAllInputWrite = 0o750

	// newBudgetInput is the new budget input the tests use.
	newBudgetInput = 2

	// observeInput is the observe input the tests use.
	observeInput = 0.05

	// port is the port the tests use.
	port = 8085

	// reaperTTL is the reaper ttl the tests use.
	reaperTTL = 90

	// seconds is the seconds the tests use.
	seconds = 2

	// total is the total the tests use.
	total = 2

	// wantAttempts is the expected attempts.
	wantAttempts = 3

	// wantGetInt is the expected get int.
	wantGetInt = 8080

	// wantMaxConcurrentConfig is the expected max concurrent config.
	wantMaxConcurrentConfig = 10

	// wantMaxRetriesConfig is the expected max retries config.
	wantMaxRetriesConfig = 3

	// wantMultiplierConfig is the expected multiplier config.
	wantMultiplierConfig = 2.0

	// writeFileInput is the write file input the tests use.
	writeFileInput = 0o600
)

// textServerPort8080Yaml is a minimal config document setting only the server port.
const textServerPort8080Yaml = "\nserver:\n  port: 8080\n"

const (
	// wantSize7 is the wantSize value 7.
	wantSize7 = 7
)

const (
	// int64Arg10 is the int64Arg value 10.
	int64Arg10 = 10

	// limit10 is the limit value 10.
	limit10 = 10

	// rows2 is the rows value 2.
	rows2 = 2

	// size2 is the size value 2.
	size2 = 2

	// wantGotBound11 is the wantGotBound value 11.
	wantGotBound11 = 11
)

const (
	// item2 is the item value 2.
	item2 = 2

	// rows3 is the rows value 3.
	rows3 = 3
)
