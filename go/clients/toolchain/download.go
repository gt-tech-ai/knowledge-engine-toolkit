package toolchain

import (
	"archive/tar"
	"archive/zip"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// maxBinarySize caps how many bytes are extracted for one binary (the largest
// pinned tool is well under it), bounding a malformed archive's decompression.
const maxBinarySize = 1 << 30

// maxDownloadSize caps how many bytes one artifact download may write to disk (the
// largest pinned archive is well under it), so a misbehaving server can't fill it.
const maxDownloadSize = 1 << 30

// windowsARM64, windowsAMD64 are the os-arch keys of the Windows ARM64 fallback:
// Windows on ARM64 runs x64 binaries under emulation, so an asset pinned only for
// windows-amd64 serves an ARM64 host too.
const (
	windowsARM64 = "windows-arm64"
	windowsAMD64 = "windows-amd64"
)

// downloadTimeout bounds one artifact fetch end to end.
const downloadTimeout = 10 * time.Minute

// Downloader fetches a remote artifact — the network seam of the pinned
// download install kinds (github-release, url). The Installer verifies whatever
// it returns against the manifest's SHA-256 before anything is placed.
//
// SDK seam — wraps net/http; unit tests inject a generated mock.
type Downloader interface {
	// Fetch opens the artifact at url for reading; the caller closes the body.
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

// WithDownloader overrides the artifact Downloader (default: an HTTPS GET that
// fails on any non-200 status), so the verified install path is testable
// without the network.
func WithDownloader(d Downloader) Option {
	return func(i *Installer) { i.downloader = d }
}

// httpDownloader is the production Downloader: a plain HTTP GET.
type httpDownloader struct {
	// client performs the request (bounded by downloadTimeout).
	client *http.Client
}

// newHTTPDownloader returns the default Downloader.
func newHTTPDownloader() Downloader {
	return httpDownloader{client: &http.Client{Timeout: downloadTimeout}}
}

// Fetch GETs url and returns the body, failing on any non-200 status so an
// error page can never be mistaken for the artifact.
func (d httpDownloader) Fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "build request for "+url)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "download "+url)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"download %s: HTTP %d", url, resp.StatusCode,
		))
	}
	return resp.Body, nil
}

// platformOS maps an install platform key to its GOOS: "linux-apt" etc. are
// all "linux".
func platformOS(platform string) string {
	if strings.HasPrefix(platform, "linux") {
		return "linux"
	}
	return platform
}

// installDownload installs a pinned download: it selects the artifact for the
// host's os-arch (a Windows ARM64 host falls back to the windows-amd64 asset), fetches
// it to a temp file while hashing, refuses it unless the
// SHA-256 matches, then extracts the binary and atomically renames it into the
// install dir. Any failure leaves nothing behind — no binary, no temp file.
func (i *Installer) installDownload(
	ctx context.Context,
	toolName string,
	dl *types.Download,
) error {
	goos := platformOS(i.platform)
	target := goos + "-" + i.arch
	artifact, ok := dl.Artifacts[target]
	if !ok && target == windowsARM64 {
		artifact, ok = dl.Artifacts[windowsAMD64]
		if ok {
			i.log.Info("no windows-arm64 asset; installing the windows-amd64 one "+
				"(runs under x64 emulation)", "tool", toolName)
		}
	}
	if !ok || artifact.SHA256 == "" {
		return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"%s %s has no pinned sha256 for %s; refusing an unverified download",
			toolName, dl.Version, target,
		))
	}

	binDir := resolveGoBin()
	if binDir == "" {
		return apperr.New(
			apperr.CodeUnavailable,
			"cannot resolve the install dir (GOBIN)",
		)
	}
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create install dir "+binDir)
	}

	archive, err := i.fetchVerified(ctx, artifact)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, fmt.Sprintf(
			"install %s %s (%s)", toolName, dl.Version, target,
		))
	}
	defer func() { _ = os.Remove(archive) }()

	binary := dl.Binary
	if goos == osWindows {
		binary += ".exe"
	}
	member := cmp.Or(artifact.Member, binary)
	if err := placeBinary(archive, artifact.URL, member, binary, binDir); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "install "+toolName)
	}
	i.log.Info("installed (sha256 verified)",
		"tool", toolName, "version", dl.Version, "path", filepath.Join(binDir, binary))
	return nil
}

// fetchVerified streams an artifact (at most maxDownloadSize bytes) into a temp file
// while hashing it and returns the file's path only when the SHA-256 matches;
// otherwise the file is removed.
func (i *Installer) fetchVerified(
	ctx context.Context,
	artifact types.Artifact,
) (string, error) {
	body, err := i.downloader.Fetch(ctx, artifact.URL)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()

	tmp, err := os.CreateTemp("", "search-tool-*")
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "create download temp file")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(
		io.MultiWriter(tmp, hash),
		io.LimitReader(body, maxDownloadSize+1),
	)
	if copyErr == nil && n > maxDownloadSize {
		copyErr = apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"artifact exceeds %d bytes", int64(maxDownloadSize),
		))
	}
	closeErr := tmp.Close()
	if err := apperr.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return "", apperr.Wrap(err, apperr.CodeUnavailable, "read "+artifact.URL)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != artifact.SHA256 {
		_ = os.Remove(tmp.Name())
		return "", apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"sha256 mismatch for %s: got %s, want %s", artifact.URL, got, artifact.SHA256,
		))
	}
	return tmp.Name(), nil
}

// placeBinary writes the binary held by a verified artifact (the raw file, the
// decompressed contents of a single-file .gz, or the entry named member inside a
// .tar.gz/.tgz/.zip, judged by the URL's suffix) to a temp file in binDir, marks
// it executable, and renames it into place as binary — so the final path only ever
// holds a complete binary.
func placeBinary(artifactPath, url, member, binary, binDir string) error {
	staged, err := os.CreateTemp(binDir, "."+binary+".tmp-*")
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "stage binary in "+binDir)
	}
	stagedPath := staged.Name()
	writeErr := extractBinary(artifactPath, url, member, staged)
	closeErr := staged.Close()
	if err := apperr.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(stagedPath)
		return err
	}

	if err := os.Chmod(stagedPath, 0o755); err != nil {
		_ = os.Remove(stagedPath)
		return apperr.Wrap(err, apperr.CodeInternal, "mark binary executable")
	}
	if err := os.Rename(stagedPath, filepath.Join(binDir, binary)); err != nil {
		_ = os.Remove(stagedPath)
		return apperr.Wrap(err, apperr.CodeInternal, "place binary "+binary)
	}
	return nil
}

// extractBinary copies the binary out of the artifact into w, dispatching on the
// artifact URL's archive suffix (anything else is the raw binary itself).
func extractBinary(artifactPath, url, binary string, w io.Writer) error {
	switch {
	case strings.HasSuffix(url, ".tar.gz"), strings.HasSuffix(url, ".tgz"):
		return extractFromTarGz(artifactPath, binary, w)
	case strings.HasSuffix(url, ".zip"):
		return extractFromZip(artifactPath, binary, w)
	case strings.HasSuffix(url, ".gz"):
		return extractFromGz(artifactPath, w)
	default:
		return copyFile(artifactPath, w)
	}
}

// extractFromGz decompresses a single-file gzip artifact (no tar wrapper — the
// decompressed stream is the binary itself) into w. It is dispatched only for a
// plain .gz suffix; .tar.gz/.tgz match the tarball case first.
func extractFromGz(src string, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open verified archive")
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "read gzip archive")
	}
	defer func() { _ = gz.Close() }()
	return boundedCopy(w, gz)
}

// copyFile copies a raw-binary artifact into w (bounded by maxBinarySize).
func copyFile(src string, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open verified artifact")
	}
	defer func() { _ = f.Close() }()
	return boundedCopy(w, f)
}

// extractFromTarGz copies the regular-file entry whose base name is binary out
// of a gzip-compressed tarball into w.
func extractFromTarGz(src, binary string, w io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open verified archive")
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "read gzip archive")
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if apperr.StdIs(err, io.EOF) {
			return missingEntry(binary)
		}
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "read tar archive")
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == binary {
			return boundedCopy(w, tr)
		}
	}
}

// extractFromZip copies the file entry whose base name is binary out of a zip
// archive into w.
func extractFromZip(src, binary string, w io.Writer) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "read zip archive")
	}
	defer func() { _ = zr.Close() }()
	for _, entry := range zr.File {
		if !entry.FileInfo().IsDir() && path.Base(entry.Name) == binary {
			return copyZipEntry(entry, w)
		}
	}
	return missingEntry(binary)
}

// copyZipEntry copies one zip entry's contents into w.
func copyZipEntry(entry *zip.File, w io.Writer) error {
	rc, err := entry.Open()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "open zip entry "+entry.Name)
	}
	defer func() { _ = rc.Close() }()
	return boundedCopy(w, rc)
}

// boundedCopy copies r into w, failing if it exceeds maxBinarySize.
func boundedCopy(w io.Writer, r io.Reader) error {
	n, err := io.CopyN(w, r, maxBinarySize+1)
	if err != nil && !apperr.StdIs(err, io.EOF) {
		return apperr.Wrap(err, apperr.CodeInternal, "extract binary")
	}
	if n > maxBinarySize {
		return apperr.New(
			apperr.CodeInvalidInput,
			"binary exceeds the extraction size cap",
		)
	}
	return nil
}

// missingEntry reports a verified archive that does not contain the binary.
func missingEntry(binary string) error {
	return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
		"verified archive has no %q entry", binary,
	))
}
