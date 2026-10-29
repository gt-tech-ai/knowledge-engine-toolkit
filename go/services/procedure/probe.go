package procedure

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The probe defaults: the first retry's wait, its cap, and the bound on one attempt.
const (
	// defaultProbeDelay is the first retry's wait when a probe sets no Delay.
	defaultProbeDelay = 500 * time.Millisecond

	// defaultProbeMaxDelay caps the retry wait when a probe sets no MaxDelay.
	defaultProbeMaxDelay = 5 * time.Second

	// probeAttemptTimeout bounds one attempt, so a hung endpoint costs one attempt
	// rather than the whole timeout.
	probeAttemptTimeout = 5 * time.Second

	// probeBodyLimit is how much of an HTTP body Contains searches.
	probeBodyLimit = 4096
)

// check is one expanded probe: its kind's target and expectations, and its retry
// schedule.
type check struct {
	// http, tcp and exec are the kind's target; exactly one is set.
	http, tcp string

	// contains is text the body or stdout must contain.
	contains string

	// hint is appended to a failure.
	hint string

	// exec is the command line.
	exec []string

	// timeout ends the retries; zero makes one attempt.
	timeout time.Duration

	// delay and maxDelay are the first retry's wait and its cap.
	delay, maxDelay time.Duration

	// status is the expected HTTP status; zero accepts any 2xx.
	status int
}

// probe runs p, retrying a failure with doubling waits until it passes or its timeout
// ends, and returns its row: a pass, or a failure carrying the last attempt's error. A
// probe that does not expand to exactly one kind fails without an attempt.
func (e *engine) probe(
	ctx context.Context,
	c *compiled,
	p *types.ProcProbe,
	name string,
	vars Vars,
) types.StepResult {
	start := time.Now()
	chk, err := c.check(ctx, p, vars)
	if err == nil {
		err = e.retry(ctx, chk)
	}
	res := c.result(name, types.StatusPass, "")
	if err != nil {
		msg := err.Error()
		if chk.hint != "" {
			msg += " (hint: " + chk.hint + ")"
		}
		res = c.result(name, types.StatusFail, msg)
	}
	res.Duration = time.Since(start)
	return res
}

// retry attempts chk until one passes, its timeout ends or ctx is done, returning the
// last attempt's error.
func (e *engine) retry(ctx context.Context, chk check) error {
	if chk.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, chk.timeout)
		defer cancel()
	}
	wait := chk.delay
	for {
		err := e.attempt(ctx, chk)
		if err == nil || chk.timeout == 0 {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		wait = min(wait*2, chk.maxDelay)
	}
}

// attempt runs chk once, bounded by probeAttemptTimeout.
func (e *engine) attempt(ctx context.Context, chk check) error {
	ctx, cancel := context.WithTimeout(ctx, probeAttemptTimeout)
	defer cancel()
	switch {
	case chk.http != "":
		return e.probeHTTP(ctx, chk)
	case chk.tcp != "":
		return e.probeTCP(ctx, chk.tcp)
	default:
		return e.probeExec(ctx, chk)
	}
}

// probeHTTP GETs chk's URL and checks its status and body.
func (e *engine) probeHTTP(ctx context.Context, chk check) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chk.http, http.NoBody)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "probe request "+chk.http)
	}
	doer := e.deps.HTTP
	if doer == nil {
		doer = http.DefaultClient
	}
	resp, err := doer.Do(req)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "HTTP GET "+chk.http)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case chk.status != 0 && resp.StatusCode != chk.status:
		return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"HTTP GET %s: status %d, want %d", chk.http, resp.StatusCode, chk.status))
	case chk.status == 0 && (resp.StatusCode < 200 || resp.StatusCode >= 300):
		return apperr.New(apperr.CodeUnavailable,
			fmt.Sprintf("HTTP GET %s: status %d", chk.http, resp.StatusCode))
	case chk.contains == "":
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "read body from "+chk.http)
	}
	if !strings.Contains(string(body), chk.contains) {
		return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"HTTP GET %s: body does not contain %q", chk.http, chk.contains))
	}
	return nil
}

// probeTCP opens and closes a connection to addr.
func (e *engine) probeTCP(ctx context.Context, addr string) error {
	var dialer interfaces.Dialer = &net.Dialer{}
	if e.deps.Dialer != nil {
		dialer = e.deps.Dialer
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "TCP connect "+addr)
	}
	_ = conn.Close()
	return nil
}

// probeExec runs chk's command line from the root and checks its exit and stdout.
func (e *engine) probeExec(ctx context.Context, chk check) error {
	r := e.deps.Runner.RunBufferedWithEnv(
		ctx,
		e.deps.Root,
		nil,
		chk.exec[0],
		chk.exec[1:]...)
	line := strings.Join(chk.exec, " ")
	out := strings.TrimSpace(string(r.Stdout) + string(r.Stderr))
	if r.Err != nil {
		return apperr.Wrap(r.Err, apperr.CodeUnavailable,
			fmt.Sprintf("%s (output: %s)", line, out))
	}
	if !strings.Contains(string(r.Stdout), chk.contains) {
		return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"%s: output does not contain %q (output: %s)", line, chk.contains, out))
	}
	return nil
}

// check expands p over vars into a check, refusing one that names no kind or several,
// a status that is not a number and a duration that does not parse.
func (c *compiled) check(
	ctx context.Context,
	p *types.ProcProbe,
	vars Vars,
) (check, error) {
	var chk check
	texts := []struct {
		dst *string
		src string
	}{
		{&chk.http, p.HTTP},
		{&chk.tcp, p.TCP},
		{&chk.contains, p.Contains},
		{&chk.hint, p.Hint},
	}
	for _, t := range texts {
		v, err := text(t.src, vars)
		if err != nil {
			return chk, err
		}
		*t.dst = v
	}
	argv, err := c.args(ctx, p.Exec, vars)
	if err != nil {
		return chk, err
	}
	chk.exec = argv
	kinds := 0
	for _, set := range []bool{chk.http != "", chk.tcp != "", len(chk.exec) > 0} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return chk, apperr.New(apperr.CodeInvalidInput,
			"a probe expands to exactly one of http, tcp, exec")
	}
	if err := probeSchedule(&chk, p, vars); err != nil {
		return chk, err
	}
	return chk, nil
}

// probeSchedule expands p's status and retry schedule into chk.
func probeSchedule(chk *check, p *types.ProcProbe, vars Vars) error {
	status, err := text(p.Status, vars)
	if err != nil {
		return err
	}
	if status != "" {
		if chk.status, err = strconv.Atoi(status); err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "probe status "+status)
		}
	}
	for _, d := range []struct {
		dst *time.Duration
		src string
		def time.Duration
	}{
		{&chk.timeout, p.Timeout, 0},
		{&chk.delay, p.Delay, defaultProbeDelay},
		{&chk.maxDelay, p.MaxDelay, defaultProbeMaxDelay},
	} {
		v, err := text(d.src, vars)
		if err != nil {
			return err
		}
		*d.dst = d.def
		if v == "" {
			continue
		}
		if *d.dst, err = time.ParseDuration(v); err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "probe duration "+v)
		}
	}
	return nil
}
