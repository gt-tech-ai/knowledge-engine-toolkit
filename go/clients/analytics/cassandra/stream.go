package cassandra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// readPlan is everything a bucket reader needs.
type readPlan struct {
	// session executes the reads.
	session interfaces.CQLSession
	// keep is the residual predicate.
	keep predicate
	// lo and hi bound the clustering time range [lo, hi).
	lo, hi time.Time
	// table is the cube/grain table.
	table string
	// org and cube are the partition-key components besides the bucket.
	org, cube string
	// fingerprint identifies the query; it is carried in every resume token.
	fingerprint string
	// groupBy are the dimensions projected into Row.Group.
	groupBy []string
	// measures are the partials projected into Row.Partials.
	measures []string
	// pageSize is the rows per page.
	pageSize int
}

// page is one fetched page of one bucket.
type page struct {
	// err is the read error, if any.
	err error
	// bucket is the bucket the page belongs to.
	bucket time.Time
	// rows are the rows that passed the residual predicate.
	rows []types.Row
	// next is the page state after this page (empty at the bucket's end).
	next []byte
}

// resumeToken is the decoded RowStream position: the bucket being read and the
// page state within it, bound to the query that issued it.
type resumeToken struct {
	// Bucket is the bucket start in Unix milliseconds.
	Bucket *int64 `json:"bucket"`
	// Query is the fingerprint of the query the token belongs to.
	Query string `json:"query"`
	// Page is the page state to continue from.
	Page []byte `json:"page,omitempty"`
}

// queryFingerprint hashes every field of q that shapes the result (all but the
// resume token), so a token resumes only the query that issued it.
func queryFingerprint(qRef *types.AggregateQuery) (string, error) {
	q := *qRef

	q.ResumeToken = nil
	//nolint:musttag // a hash input, never a wire format: untagged field names are fine
	data, err := json.Marshal(q)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"analytics: query is not serializable",
		)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:16]), nil
}

// decodeToken parses a resume token (nil for none); a malformed one, or one
// issued for a query other than fingerprint, is CodeInvalidInput.
func decodeToken(data []byte, fingerprint string) (*resumeToken, error) {
	if len(data) == 0 {
		return nil, nil //nolint:nilnil // no token is a valid "start from the beginning"
	}

	var currentResumeToken resumeToken

	err := json.Unmarshal(data, &currentResumeToken)
	if err != nil || currentResumeToken.Bucket == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: malformed resume token",
		)
	}

	if currentResumeToken.Query != fingerprint {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: resume token belongs to a different query",
		)
	}

	return &currentResumeToken, nil
}

// fetch reads one page of bucket starting at state.
func (p *readPlan) fetch(ctx context.Context, bucket time.Time, state []byte) page {
	iter := p.session.Query(
		"SELECT ts, dims, partials FROM "+p.table+
			" WHERE org_id = ? AND cube = ? AND bucket = ? AND ts >= ? AND ts < ?",
		p.org, p.cube, bucket, p.lo, p.hi,
	).WithContext(ctx).PageSize(p.pageSize).PageState(state).Idempotent(true).Iter()
	out := page{bucket: bucket, err: nil, rows: nil, next: nil}

	var (
		instant  time.Time
		dims     map[string]string
		partials map[string][]byte
	)

	for iter.Scan(&instant, &dims, &partials) {
		if p.keep(stored{ts: instant.UTC(), dims: dims}) {
			out.rows = append(out.rows, p.project(instant, dims, partials))
		}

		instant, dims, partials = time.Time{}, nil, nil
	}

	out.next = iter.PageState()
	out.err = iter.Close()

	return out
}

// project keeps the group-by dimensions and the requested partials of a row.
func (p *readPlan) project(
	ts time.Time,
	dims map[string]string,
	partials map[string][]byte,
) types.Row {
	row := types.Row{
		TS:       ts.UTC(),
		Group:    make(map[string]string, len(p.groupBy)),
		Partials: make(map[string][]byte, len(p.measures)),
	}
	for _, name := range p.groupBy {
		row.Group[name] = dims[name]
	}

	for _, name := range p.measures {
		if blob, ok := partials[name]; ok {
			row.Partials[name] = blob
		}
	}

	return row
}

// stream delivers the buckets' pages in bucket order while up to window buckets
// are read ahead concurrently, each into a one-page channel. A page is
// materialized whole, so memory is bounded by about 2×window pages of pageSize
// rows (one buffered and one being fetched per reader), and a slow consumer
// stalls the readers. The first read error is terminal.
type stream struct {
	// ctx scopes the readers; cancel stops them.
	ctx    context.Context //nolint:containedctx // the stream owns its readers' context
	cancel context.CancelFunc
	// plan reads pages.
	plan *readPlan
	// chans are the per-bucket page channels, started lazily within the window.
	chans []chan page
	// buckets are the bucket starts, in order.
	buckets []time.Time
	// err is the first read error; once set, every Next returns it.
	err error
	// token is the position after the last delivered page.
	token []byte
	// first is the page state to resume the first bucket from.
	first []byte
	// wg tracks the readers.
	wg sync.WaitGroup
	// cur is the bucket being delivered; started counts readers started.
	cur, started int
	// window bounds read-ahead.
	window int
	// closed reports Close was called.
	closed bool
}

// Compile-time interface assertion.
var _ interfaces.RowStream = (*stream)(nil)

// newStream positions a stream at resume (or the first bucket) and starts the
// first window of readers.
func newStream(
	ctx context.Context,
	plan *readPlan,
	buckets []time.Time,
	resume *resumeToken,
	window int,
) *stream {
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	currentStream := &stream{
		ctx:     sctx,
		cancel:  cancel,
		plan:    plan,
		window:  window,
		chans:   nil,
		buckets: nil,
		err:     nil,
		token:   nil,
		first:   nil,
		wg:      sync.WaitGroup{},
		cur:     0,
		started: 0,
		closed:  false,
	}

	for index, b := range buckets {
		if resume != nil && b.UnixMilli() < *resume.Bucket {
			continue
		}

		if resume != nil && b.UnixMilli() == *resume.Bucket {
			currentStream.first = resume.Page
		}

		currentStream.buckets = buckets[index:]

		break
	}

	currentStream.chans = make([]chan page, len(currentStream.buckets))
	currentStream.fill()

	return currentStream
}

// Next returns the next page in bucket order. After a read error it returns
// that error, with no rows and more=false, on every call.
func (s *stream) Next(ctx context.Context) ([]types.Row, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}

	for s.cur < len(s.buckets) {
		secondPage, ok, err := s.receive(ctx)
		if err != nil {
			return nil, false, err
		}

		if !ok {
			s.cur++
			s.fill()

			continue
		}

		s.token = s.position(&secondPage)

		return secondPage.rows, len(secondPage.next) > 0 || s.cur+1 < len(s.buckets), nil
	}

	return nil, false, nil
}

// ResumeToken returns the position after the last delivered page (nil when the
// stream is exhausted or nothing was delivered).
func (s *stream) ResumeToken() []byte { return s.token }

// Close stops the readers and waits for them; it is idempotent.
func (s *stream) Close() error {
	if s.closed {
		return nil
	}

	s.closed = true
	s.cancel()
	s.wg.Wait()

	return nil
}

// receive waits for the current bucket's next page; false means the bucket is drained.
// A page's error sticks to the stream; a canceled wait does not.
func (s *stream) receive(ctx context.Context) (page, bool, error) {
	select {
	case currentPage, found := <-s.chans[s.cur]:
		if found && currentPage.err != nil {
			s.err = currentPage.err

			return page{}, false, s.err
		}

		return currentPage, found, nil
	case <-ctx.Done():
		return page{}, false, apperr.Wrap(
			ctx.Err(),
			apperr.CodeCanceled,
			"analytics: stream read canceled",
		)
	}
}

// fill starts readers until window buckets ahead of cur are in flight.
func (s *stream) fill() {
	for s.started < len(s.buckets) && s.started < s.cur+s.window {
		index := s.started
		channel := make(chan page, 1)

		s.chans[index] = channel

		state := []byte(nil)

		if index == 0 {
			state = s.first
		}

		s.wg.Add(1)

		go s.read(s.buckets[index], state, channel)

		s.started++
	}
}

// read fetches every page of one bucket into ch, then closes it.
func (s *stream) read(bucket time.Time, state []byte, channel chan<- page) {
	defer s.wg.Done()
	defer close(channel)

	for {
		currentPage := s.plan.fetch(s.ctx, bucket, state)
		select {
		case channel <- currentPage:
		case <-s.ctx.Done():
			return
		}

		if currentPage.err != nil || len(currentPage.next) == 0 {
			return
		}

		state = currentPage.next
	}
}

// position encodes the token after page p: the same bucket's next page, or the
// following bucket's start.
func (s *stream) position(currentPage *page) []byte {
	atMs := currentPage.bucket.UnixMilli()
	currentResumeToken := resumeToken{
		Bucket: &atMs,
		Query:  s.plan.fingerprint,
		Page:   currentPage.next,
	}

	if len(currentPage.next) == 0 {
		if s.cur+1 >= len(s.buckets) {
			return nil
		}

		nextMs := s.buckets[s.cur+1].UnixMilli()

		currentResumeToken = resumeToken{
			Bucket: &nextMs,
			Query:  s.plan.fingerprint,
			Page:   nil,
		}
	}

	//nolint:errchkjson // a string and a byte slice always marshal
	b, _ := json.Marshal(currentResumeToken)

	return b
}
