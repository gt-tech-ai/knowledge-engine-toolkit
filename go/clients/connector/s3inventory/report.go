package s3inventory

import (
	"context"
	"fmt"
	"path"
	"strings"

	connectors3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// checksumName is the file S3 Inventory writes beside a finished manifest.json: its
// presence marks the report under that dated folder complete.
const checksumName = "manifest.checksum"

// folderOf returns the report folder of cfg with its trailing slash, or "" when a
// manifest key pins one report.
func folderOf(cfg *interfaces.SourceConfig) string {
	if cfg.InventoryManifestKey != "" || cfg.InventoryPrefix == "" {
		return ""
	}

	return strings.TrimSuffix(cfg.InventoryPrefix, textSlash) + textSlash
}

// splitToken splits a folder-mode token "<stamp>/<file>:<row>" into its report and
// position; a pinned-key token is all position. A folder-mode token with no report is
// CodeInvalidInput.
func (s *Source) splitToken(token string) (stamp, position string, err error) {
	if s.folder == "" || token == "" {
		return "", token, nil
	}

	stamp, position, found := strings.Cut(token, textSlash)
	if !found || stamp == "" {
		return "", "", apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"connector s3_inventory: malformed continuation token %q", token,
		))
	}

	return stamp, position, nil
}

// token is the continuation token at file and row, naming the report in folder mode.
func (s *Source) token(file, row int) string {
	position := fmt.Sprintf("%d:%d", file, row)
	if s.folder == "" {
		return position
	}

	return s.stamp + textSlash + position
}

// chooseReport points the source at its report in folder mode: the token's (stamp), else
// the one already chosen, else the newest complete one under the folder. Switching report
// drops the parsed manifest and the open data file.
func (s *Source) chooseReport(ctx context.Context, stamp string) error {
	if s.folder == "" {
		return nil
	}

	if stamp == "" {
		if s.stamp != "" {
			return nil
		}

		newest, err := s.newestReport(ctx)
		if err != nil {
			return err
		}

		stamp = newest
	}

	if stamp == s.stamp {
		return nil
	}

	s.release()

	s.report, s.stamp = nil, stamp
	s.manifestKey = s.folder + stamp + "/manifest.json"

	return nil
}

// newestReport pages the folder's listing (one page in memory at a time) and returns the
// greatest dated folder holding a manifest.checksum directly under it; the stamps sort
// by time as text (YYYY-MM-DDTHH-MMZ). A folder with none is CodeNotFound: the first
// report has not been delivered yet.
func (s *Source) newestReport(ctx context.Context) (string, error) {
	var newest, token string

	for {
		objs, next, err := s.storage.ListObjectsPageToken(
			ctx, s.reportBucket, s.folder, token, defaultPageSize,
		)
		if err != nil {
			return "", connectors3.ClassifyListError(err)
		}

		newest = s.newestStamp(objs, newest)

		if next == "" {
			break
		}

		token = next
	}

	if newest == "" {
		return "", apperr.New(apperr.CodeNotFound, fmt.Sprintf(
			"connector s3_inventory: no complete report under %q", s.folder,
		))
	}

	return newest, nil
}

// newestStamp returns the latest report stamp among objs and newest; only a stamped
// folder holding the checksum file is a complete report.
func (s *Source) newestStamp(objs []interfaces.StorageObject, newest string) string {
	for i := range objs {
		dir, name := path.Split(strings.TrimPrefix(objs[i].Key, s.folder))
		stamp := strings.TrimSuffix(dir, textSlash)

		complete := name == checksumName && stamp != "" &&
			!strings.Contains(stamp, textSlash)
		if complete && stamp > newest {
			newest = stamp
		}
	}

	return newest
}
