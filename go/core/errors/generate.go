package errors

// codes.go and the Python ErrorCode module are rendered from taxonomy/error-codes.yaml:
// edit the YAML and run `go generate ./go/core/errors/...`, never the outputs. The
// generator imports this package, so it runs against the committed codes.go; regenerate
// rather than deleting it.
//go:generate go run ../../tools/errcodes/cmd/errcodes -root ../../..
