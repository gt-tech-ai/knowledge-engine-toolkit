package unit_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/procedure"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// response is an HTTP response with status code and body.
func response(code int, body string) *http.Response {
	return fixtures.BuildRef(func(row *http.Response) {
		row.StatusCode = code
		row.Body = io.NopCloser(strings.NewReader(body))
	})
}

// TestServicesProcedure_ProbeKinds tests the probe step's three kinds over a mixed list.
//
// Why this test is important:
//   - A health check over a stack is a list of mixed checks (an HTTP endpoint, a port, a
//     query run in a container); one probe row over the list must run each item as its
//     own kind and judge it by its own expectations, or a smoke run passes a dead
//     service.
//
// What it tests:
//   - One concurrent Each row probes an HTTP item (status and body checked), a TCP item
//     and an Exec item (stdout checked), one row per item in the items' order.
//   - An HTTP item with no status accepts a 2xx and fails a 503; an expected 401
//     passes; a body without Contains, an Exec stdout without Contains, and a refused
//     TCP connection fail with the hint appended.
func TestServicesProcedure_ProbeKinds(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	doer := mocks.NewMockHTTPDoer(ctrl)
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/ok":
				return response(http.StatusOK, "status: ok"), nil
			case "/auth":
				return response(http.StatusUnauthorized, ""), nil
			case "/body":
				return response(http.StatusOK, textNope), nil
			default:
				return response(http.StatusServiceUnavailable, ""), nil
			}
		}).
		AnyTimes()

	dialer := mocks.NewMockDialer(ctrl)
	dialer.EXPECT().DialContext(gomock.Any(), textTCP, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr == "db:5432" {
				client, server := net.Pipe()

				_ = server.Close()

				return client, nil
			}

			return nil, apperr.Sentinel("connection refused")
		}).AnyTimes()

	testServicesProcedureProbeKindsRunsProbes(t, doer, dialer)
}

// testServicesProcedureProbeKindsRunsProbes continues TestServicesProcedure_ProbeKinds.
func testServicesProcedureProbeKindsRunsProbes(
	t *testing.T,
	doer *mocks.MockHTTPDoer,
	dialer *mocks.MockDialer,
) {
	t.Helper()

	probe := fixtures.BuildRef(func(row *types.ProcProbe) {
		row.HTTP = "{item.http}"
		row.TCP = "{item.tcp}"
		row.Exec = []string{"{item.exec...}"}
		row.Status = "{item.status}"
		row.Contains = "{item.contains}"
		row.Hint = "{item.hint}"
	})
	procs := []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = "smoke"
		row.Inputs = []string{textChecks}
		row.Steps = []types.ProcStep{fixtures.Build(func(row *types.ProcStep) {
			row.Name = "{item.name}"
			row.Each = textChecks
			row.Concurrent = true
			row.Probe = probe
		})}
	})}
	rec := fixtures.BuildRef(func(row *recorder) {
		row.out = map[string]string{
			"psql -c select": "3 rows", "psql -c empty": "0 rows",
		}
	})
	eng := procEngine(
		t,
		rec,
		procs,
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.HTTP = doer
			row.Dialer = dialer
		}),
	)

	testServicesProcedureProbeKindsStatuses(t, eng)
}

// testServicesProcedureProbeKindsStatuses continues TestServicesProcedure_ProbeKinds:
// statuses.
func testServicesProcedureProbeKindsStatuses(
	t *testing.T,
	eng interfaces.ProcedureRunner,
) {
	t.Helper()

	check := func(name string, items ...any) map[string]any {
		statusByName := map[string]any{
			textName: name, "http": "", textTCP: "", "exec": []any{},
			textStatus: "", "contains": "", "hint": "",
		}
		for i := 0; i+1 < len(items); i += initialI {
			statusByName[asType[string](t, items[i])] = items[i+1]
		}

		return statusByName
	}

	res, err := eng.Run(t.Context(), "smoke", procedure.Vars{textChecks: []any{
		check(textAPI, "http", "http://api/ok", "contains", textOk),
		check("kong", "http", "http://kong/auth", textStatus, text401),
		check(textDown, "http", "http://down/x"),
		check(textBody, "http", "http://body/body", "contains", textOk),
		check(textDB, textTCP, "db:5432"),
		check(textRedis, textTCP, "redis:6379", "hint", "is redis up?"),
		check(textSeed, "exec", []any{textPsql, "-c", "select"}, "contains", "3 rows"),
		check(textEmpty, "exec", []any{textPsql, "-c", textEmpty}, "contains", "3 rows"),
	}}, stdx.Zero[types.RunOptions]())

	require.NoError(t, err)

	names := make([]string, 0, len(res))

	testServicesProcedureProbeKindsChecksStatuses(t, res, names)
}

// testServicesProcedureProbeKindsChecksStatuses continues
// testServicesProcedureProbeKindsStatuses.
func testServicesProcedureProbeKindsChecksStatuses(
	t *testing.T,
	res types.StepResults,
	names []string,
) {
	t.Helper()

	for i := range res {
		names = append(names, res[i].Name)
	}

	assert.Equal(
		t,
		[]string{
			textAPI,
			"kong",
			textDown,
			textBody,
			textDB,
			textRedis,
			textSeed,
			textEmpty,
		},
		names,
	)

	statusOf := statuses(res)
	for _, pass := range []string{textAPI, "kong", textDB, textSeed} {
		assert.Equal(t, types.StatusPass, statusOf[pass], pass)
	}

	for _, fail := range []string{textDown, textBody, textRedis, textEmpty} {
		assert.Equal(t, types.StatusFail, statusOf[fail], fail)
	}

	assert.Contains(t, res[2].Error, "status 503")
	assert.Contains(t, res[3].Error, `does not contain "ok"`)
	assert.Contains(t, res[5].Error, "is redis up?")
	assert.Contains(t, res[7].Error, `does not contain "3 rows"`)
}

// lateProbeResponse answers the late host OK from its third attempt; every other
// request gets a bad gateway.
func lateProbeResponse(attempts *atomic.Int32, r *http.Request) *http.Response {
	if r.URL.Host == textLate && attempts.Add(1) >= wantAttempts {
		return response(http.StatusOK, "")
	}

	return response(http.StatusBadGateway, "")
}

// singleProbeProcedure returns a procedure with one probe step named after it.
func singleProbeProcedure(name string, set func(row *types.ProcProbe)) types.Procedure {
	return fixtures.Build(func(row *types.Procedure) {
		row.Name = name
		row.Steps = []types.ProcStep{fixtures.Build(func(step *types.ProcStep) {
			step.Name = name
			step.Probe = fixtures.BuildRef(set)
		})}
	})
}

// probeRetryProcedures returns the procedures the probe retry test runs.
func probeRetryProcedures() []types.Procedure {
	retrying := func(url, timeout string) func(row *types.ProcProbe) {
		return func(row *types.ProcProbe) {
			row.HTTP = url
			row.Timeout = timeout
			row.Delay = "1ms"
			row.MaxDelay = "2ms"
		}
	}

	none := singleProbeProcedure(
		textNone,
		func(row *types.ProcProbe) { row.HTTP = "{url}" },
	)

	none.Inputs = []string{textURL}

	return []types.Procedure{
		singleProbeProcedure(textLate, retrying("http://late/", "2s")),
		singleProbeProcedure(textDead, retrying("http://dead/", "30ms")),
		none,
		singleProbeProcedure(textTwo, func(row *types.ProcProbe) {
			row.HTTP = "http://a/"
			row.TCP = "a:1"
		}),
	}
}

// TestServicesProcedure_ProbeRetriesUntilTimeout tests the probe's retry loop.
//
// Why this test is important:
//   - A stack that was just started answers only after a while; a probe that gives up on
//     the first refusal fails a healthy start, and one that never gives up hangs the run.
//
// What it tests:
//   - A probe that fails twice and then passes is one passing row after three attempts.
//   - A probe that never passes fails once its timeout ends, reporting the last error.
//   - A probe naming no kind, or two, fails its row with CodeInvalidInput text.
func TestServicesProcedure_ProbeRetriesUntilTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	var attempts atomic.Int32

	doer := mocks.NewMockHTTPDoer(ctrl)
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(r *http.Request) (*http.Response, error) {
			return lateProbeResponse(&attempts, r), nil
		}).
		AnyTimes()

	procs := probeRetryProcedures()
	eng := procEngine(
		t,
		new(recorder),
		procs,
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.HTTP = doer
		}),
	)

	res, err := eng.Run(t.Context(), textLate, nil, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(t, types.StatusPass, statuses(res)[textLate])
	assert.Equal(t, int32(wantAttempts), attempts.Load())

	res, err = eng.Run(t.Context(), textDead, nil, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, statuses(res)[textDead])
	assert.Contains(t, res[0].Error, "status 502")

	for _, name := range []string{textNone, textTwo} {
		res, err = eng.Run(
			t.Context(),
			name,
			procedure.Vars{textURL: ""},
			stdx.Zero[types.RunOptions](),
		)
		require.NoError(t, err)
		assert.Equal(t, types.StatusFail, statuses(res)[name], name)
		assert.Contains(t, res[0].Error, "exactly one of http, tcp, exec", name)
	}
}

// TestServicesProcedure_ProbeIsAStepKind tests that compile treats probe as a step kind.
//
// Why this test is important:
//   - A row that both probes and runs a tool is ambiguous; refusing it at build keeps a
//     typo from silently dropping one of the two.
//
// What it tests:
//   - A step with both probe and tool fails NewFromConfig with CodeInvalidInput; a probe
//     with no kind field at all fails the same way.
func TestServicesProcedure_ProbeIsAStepKind(t *testing.T) {
	t.Parallel()

	for name, procStep := range map[string]types.ProcStep{
		"probe and tool": fixtures.Build(func(row *types.ProcStep) {
			row.Name = textX
			row.Tool = textEcho
			row.Probe = fixtures.BuildRef(func(row *types.ProcProbe) {
				row.TCP = "a:1"
			})
		}),
		"empty probe": fixtures.Build(func(row *types.ProcStep) {
			row.Name = textX
			row.Probe = fixtures.BuildRef(func(row *types.ProcProbe) {
				row.Contains = textOk
			})
		}),
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			fixtures.Build(func(row *types.Procedure) {
				row.Name = textP
				row.Steps = []types.ProcStep{procStep}
			}),
		}}, new(procedure.Deps))
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}
}

const (
	// textDead is the literal "dead".
	textDead = "dead"

	// textLate is the literal "late".
	textLate = "late"
)
