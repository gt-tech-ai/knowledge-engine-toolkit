// Package s3inventory is the s3_inventory ConnectorSource: it reads a bucket's objects
// from its S3 Inventory report (manifest.json, then the gzipped CSV data files it lists)
// instead of listing the bucket. Pages and their objects match the s3 kind's for the same
// objects; the continuation token is "<data file index>:<rows consumed>", so a crashed
// sync resumes inside a data file. Given the report's folder instead of one manifest, it
// reads the newest complete report there and prefixes each token with that report's
// dated folder (report.go). It authenticates exactly like the s3 kind (through
// connector/s3's NewStorage).
package s3inventory

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	connectors3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultPageSize is the page a non-positive limit gets (the s3 kind's listing page).
const defaultPageSize = 1000

// Source is the s3_inventory ConnectorSource.
type Source struct {
	// storage reads the report (the connector-scoped client).
	storage interfaces.StorageClient
	// report is the parsed manifest, read once.
	report *manifest
	// cols is the report's column index.
	cols schema
	// open is the data file left open at the last page's end, reused when the next page
	// starts there (otherwise every page would re-read its file from the top).
	open *cursor
	// bucket is the source bucket the report must list.
	bucket string
	// prefix keeps only keys under it.
	prefix string
	// reportBucket holds the report.
	reportBucket string
	// manifestKey is the report's manifest.json key (in folder mode, the chosen report's).
	manifestKey string
	// folder is the report's configuration folder; empty pins manifestKey (folder mode
	// is on when it is set).
	folder string
	// stamp is the dated report folder read in folder mode, named in every token.
	stamp string
	// mu serializes ListPage (a sync crawls one source from one goroutine; this keeps a
	// concurrent caller from corrupting the open cursor).
	mu sync.Mutex
}

// cursor is an open data file positioned after row rows.
type cursor struct {
	// body is the downloaded object, closed with the cursor.
	body io.ReadCloser
	// rows decodes the (gunzipped) CSV.
	rows *csv.Reader
	// pending is a row read ahead (to learn whether the file has more) and not yet
	// consumed; row does not count it.
	pending []string
	// file is the data file's index in the manifest.
	file int
	// row is the number of rows consumed.
	row int
}

// Ensure Source satisfies the ConnectorSource contract.
var _ interfaces.ConnectorSource = (*Source)(nil)

// NewSource builds the s3_inventory source for cfg. It fails loud (CodeInvalidInput),
// before any call, with neither a manifest key nor a report folder, or on an auth config
// the s3 kind refuses.
// cfg must not be nil.
func NewSource(
	ctx context.Context,
	cfg *interfaces.SourceConfig,
	stsClient stscreds.AssumeRoleAPIClient,
	build connectors3.ClientBuilder,
) (*Source, error) {
	if cfg.InventoryManifestKey == "" && cfg.InventoryPrefix == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"connector s3_inventory: no inventory manifest key or report folder",
		)
	}

	storage, err := connectors3.NewStorage(ctx, cfg, stsClient, build)
	if err != nil {
		return nil, err
	}

	reportBucket := cfg.InventoryBucket
	if reportBucket == "" {
		reportBucket = cfg.Bucket
	}

	return &Source{
		storage:      storage,
		bucket:       cfg.Bucket,
		prefix:       cfg.Prefix,
		reportBucket: reportBucket,
		manifestKey:  cfg.InventoryManifestKey,
		folder:       folderOf(cfg),
	}, nil
}

// TestConnection reads the manifest and the first page, and returns that page's object
// count (bounded like the s3 kind's probe).
func (s *Source) TestConnection(ctx context.Context) (int, error) {
	objs, _, err := s.ListPage(ctx, "", defaultPageSize)

	return len(objs), err
}

// ListPage returns up to limit objects after continuationToken ("" = the report's start)
// and the token to resume from ("" when the report is exhausted). In folder mode the
// token names its report ("<stamp>/<file>:<row>"), so a resumed crawl finishes the report
// it started even after a newer one lands. An object-store failure is classified like the
// s3 kind's; a malformed token or report is CodeInvalidInput, and a folder with no
// complete report CodeNotFound.
func (s *Source) ListPage(
	ctx context.Context,
	continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = defaultPageSize
	}

	file, row, err := s.resume(ctx, continuationToken)
	if err != nil {
		return nil, "", err
	}

	var page []interfaces.StorageObject

	for file < len(s.report.Files) && len(page) < limit {
		cur, err := s.readFrom(ctx, file, row, &page, limit)
		if err != nil {
			s.release()

			return nil, "", err
		}

		file, row = cur.nextPosition()
	}

	if file >= len(s.report.Files) {
		return page, "", nil
	}

	return page, s.token(file, row), nil
}

// nextPosition is where the next read resumes: the next file once this one is drained.
func (c *cursor) nextPosition() (file, row int) {
	if c.rows == nil {
		return c.file + 1, 0
	}

	return c.file, c.row
}

// resume loads the report continuationToken names and returns the data file and row it
// points at.
func (s *Source) resume(
	ctx context.Context,
	continuationToken string,
) (file, row int, err error) {
	stamp, position, err := s.splitToken(continuationToken)
	if err != nil {
		return 0, 0, err
	}

	file, row, err = parseToken(position)
	if err != nil {
		return 0, 0, err
	}

	err = s.chooseReport(ctx, stamp)
	if err != nil {
		return 0, 0, err
	}

	err = s.loadReport(ctx)
	if err != nil {
		return 0, 0, err
	}

	return file, row, nil
}

// readFrom fills *page from the data file at index file, after row rows. A cursor left
// open by an earlier call may hold a body whose request context has since ended, so a
// read failure
// on it is retried once on a fresh download.
func (s *Source) readFrom(
	ctx context.Context,
	file, row int,
	page *[]interfaces.StorageObject,
	limit int,
) (*cursor, error) {
	reused := s.open != nil && s.open.file == file && s.open.row == row

	cur, err := s.cursorAt(ctx, file, row)
	if err != nil {
		return nil, err
	}

	filled, err := s.fill(cur, *page, limit)
	if err != nil && reused {
		s.release()

		cur, err = s.cursorAt(ctx, file, row)
		if err != nil {
			return nil, err
		}

		filled, err = s.fill(cur, *page, limit)
	}

	if err != nil {
		return nil, err
	}

	*page = filled

	return cur, nil
}

// fill appends objects from cur until the page holds limit or the file ends (then cur
// is closed and its rows set to nil). A full page reads one row ahead: at the file's end
// the cursor closes, so the page's token moves to the next file (or ends) instead of
// pointing at an empty remainder.
func (s *Source) fill(
	cur *cursor,
	page []interfaces.StorageObject,
	limit int,
) ([]interfaces.StorageObject, error) {
	for len(page) < limit {
		row, err := s.nextObject(cur)
		if err != nil {
			return nil, err
		}

		if row.done {
			return page, nil
		}

		if row.keep {
			page = append(page, row.object)
		}
	}

	s.readAhead(cur)

	return page, nil
}

// inventoryRow is the outcome of reading one inventory row.
type inventoryRow struct {
	// object is the row's object, meaningful only when keep is true.
	object interfaces.StorageObject
	// keep reports a row the listing keeps.
	keep bool
	// done reports the file's end, in which case no row was read.
	done bool
}

// nextObject reads one row and reports what it held.
func (s *Source) nextObject(cur *cursor) (inventoryRow, error) {
	record, err := next(cur)
	if apperr.StdIs(err, io.EOF) {
		s.release()

		return inventoryRow{done: true}, nil
	}

	if err != nil {
		return inventoryRow{}, err
	}

	cur.row++

	obj, keep, err := s.cols.object(record, s.bucket, s.prefix)

	return inventoryRow{object: obj, keep: keep}, err
}

// readAhead peeks one row so a page ending at the file's end releases it. Any other
// read-ahead failure is left for the next page, which retries a fresh body.
func (s *Source) readAhead(cur *cursor) {
	ahead, err := cur.rows.Read()

	switch {
	case apperr.StdIs(err, io.EOF):
		s.release()
	case err == nil:
		cur.pending = ahead
	default:
		// No other value changes the outcome.
	}
}

// next returns cur's read-ahead row when there is one, else the next row of the file:
// io.EOF at the file's end, a coded error (readError) on a failure.
func next(cur *cursor) ([]string, error) {
	if cur.pending != nil {
		record := cur.pending

		cur.pending = nil

		return record, nil
	}

	record, err := cur.rows.Read()
	if apperr.StdIs(err, io.EOF) {
		return nil, io.EOF
	}

	if err != nil {
		return nil, readError(err)
	}

	return record, nil
}

// loadReport reads and validates the manifest once.
func (s *Source) loadReport(ctx context.Context) error {
	if s.report != nil {
		return nil
	}

	body, err := s.storage.Download(ctx, s.reportBucket, s.manifestKey)
	if err != nil {
		return connectors3.ClassifyListError(err)
	}

	defer func() { _ = body.Close() }()

	report, cols, err := parseManifest(body, s.bucket)
	if err != nil {
		return err
	}

	s.report, s.cols = report, cols

	return nil
}

// cursorAt returns a cursor on the data file at index file, after row rows: the open
// one when it is there, else the file opened afresh and row rows skipped.
func (s *Source) cursorAt(ctx context.Context, file, row int) (*cursor, error) {
	if s.open != nil && s.open.file == file && s.open.row == row {
		return s.open, nil
	}

	s.release()

	key := s.report.Files[file].Key

	body, err := s.storage.Download(ctx, s.reportBucket, key)
	if err != nil {
		return nil, connectors3.ClassifyListError(err)
	}

	in, err := dataReader(body, key)
	if err != nil {
		_ = body.Close()

		return nil, err
	}

	rows := csv.NewReader(in)

	rows.FieldsPerRecord = -1
	rows.ReuseRecord = false

	cur := &cursor{file: file, body: body, rows: rows}

	s.open = cur

	for cur.row < row {
		_, err := rows.Read()
		if err != nil {
			s.release()

			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"connector s3_inventory: token past the data file's end",
			)
		}

		cur.row++
	}

	return cur, nil
}

// dataReader returns body as plain text, gunzipping it when key names a .gz file.
func dataReader(body io.Reader, key string) (io.Reader, error) {
	if !strings.HasSuffix(key, ".gz") {
		return body, nil
	}

	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"connector s3_inventory: gunzip data file",
		)
	}

	return gz, nil
}

// release closes the open data file, if any; its cursor's rows become nil.
func (s *Source) release() {
	if s.open == nil {
		return
	}

	_ = s.open.body.Close()
	s.open.rows = nil
	s.open = nil
}

// readError classifies a data-file read failure: a malformed CSV row is
// CodeInvalidInput; anything else (the body's transport) is transient CodeUnavailable.
func readError(err error) error {
	var parseErr *csv.ParseError

	if apperr.As(err, &parseErr) {
		return apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"connector s3_inventory: malformed data file",
		)
	}

	return apperr.Wrap(
		err,
		apperr.CodeUnavailable,
		"connector s3_inventory: read data file",
	)
}

// parseToken splits "<file>:<row>"; "" is the start.
func parseToken(token string) (file, row int, err error) {
	if token == "" {
		return 0, 0, nil
	}

	fileText, rowText, found := strings.Cut(token, ":")
	file, fileErr := strconv.Atoi(fileText)
	row, rowErr := strconv.Atoi(rowText)

	if !found || fileErr != nil || rowErr != nil || file < 0 || row < 0 {
		return 0, 0, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"connector s3_inventory: malformed continuation token %q", token,
		))
	}

	return file, row, nil
}
