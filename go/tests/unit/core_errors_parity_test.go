package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rpc"
)

// codesJSONPath returns the path of the committed cross-language parity table. The
// Python suite reads the same file, so a Go mapping change that is not mirrored in
// Python fails there.
func codesJSONPath() string {
	return filepath.Join("..", "..", "core", "errors", "testdata", "codes.json")
}

// allErrorCodes lists every ErrorCode the Go errors package declares. The Python
// parity test asserts its ErrorCode set equals this table's keys, so a code added
// here without its Python counterpart (or the reverse) fails one side.
func allErrorCodes() []apperr.ErrorCode {
	return []apperr.ErrorCode{
		apperr.CodeUnknown,
		apperr.CodeInternal,
		apperr.CodeNotFound,
		apperr.CodeUnauthorized,
		apperr.CodeForbidden,
		apperr.CodeInvalidInput,
		apperr.CodeConflict,
		apperr.CodeTimeout,
		apperr.CodeCanceled,
		apperr.CodeUnavailable,
		apperr.CodeIngestion,
		apperr.CodeQualityFailed,
		apperr.CodeUpstream,
		apperr.CodeResourceExhausted,
	}
}

// codeRow is one code's transport mapping and retry classification.
type codeRow struct {
	// GRPC is the gRPC/Connect status code rpc.Sanitize returns for the code.
	GRPC int `json:"grpc"`
	// HTTP is the HTTP status ToHTTPStatus returns for the code.
	HTTP int `json:"http"`
	// Transient is IsTransient for an error carrying the code.
	Transient bool `json:"transient"`
	// Permanent is IsPermanent for an error carrying the code.
	Permanent bool `json:"permanent"`
}

// TestCodesJSON_MatchesClassifyMaps tests that the committed codes.json equals the
// table regenerated from the live Go maps.
//
// Why this test is important:
//   - codes.json is what the Python suite compares its own maps against; if it
//     drifts from the Go maps, the cross-language parity check passes on stale data
//
// What it tests:
//   - For every Go ErrorCode, the regenerated {grpc, http, transient, permanent}
//     row equals the committed file byte-for-byte (set KE_UPDATE_GOLDEN=1 to rewrite it)
func TestCodesJSON_MatchesClassifyMaps(t *testing.T) {
	t.Parallel()

	table := make(map[apperr.ErrorCode]codeRow, len(allErrorCodes()))
	for _, code := range allErrorCodes() {
		err := apperr.New(code, "parity")
		grpc, _ := rpc.Sanitize(err)
		table[code] = codeRow{
			GRPC:      int(grpc),
			HTTP:      apperr.ToHTTPStatus(code),
			Transient: apperr.IsTransient(err),
			Permanent: apperr.IsPermanent(err),
		}
	}
	got, err := json.MarshalIndent(table, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	if os.Getenv("KE_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(codesJSONPath(), got, 0o600))
	}
	want, err := os.ReadFile(codesJSONPath())
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}
