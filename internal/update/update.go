// Package update installs published Hane binaries without touching account data.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const repository = "https://github.com/dmillzilla/hane"
const maxArchiveSize = 64 << 20
const maxBinarySize = 64 << 20

var errNotFound = errors.New("not found")

type release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type updater struct {
	client                              *http.Client
	latestURL, downloadBase, executable string
	goos, goarch, current               string
	out                                 io.Writer
}

// Run updates the executable through which Hane was launched. Downloads are
// anonymous HTTPS requests; no mail settings or credentials are loaded.
func Run(ctx context.Context, current string, out io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate Hane: %w", err)
	}
	u := updater{
		client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) >= 10 {
				return errors.New("refused an insecure or excessive download redirect")
			}
			return nil
		}},
		latestURL:    "https://api.github.com/repos/dmillzilla/hane/releases/latest",
		downloadBase: repository + "/releases/download",
		executable:   executable, goos: runtime.GOOS, goarch: runtime.GOARCH,
		current: current, out: out,
	}
	return u.run(ctx)
}

func (u updater) run(ctx context.Context) error {
	if (u.goos != "linux" && u.goos != "darwin") || (u.goarch != "amd64" && u.goarch != "arm64") {
		return fmt.Errorf("automatic updates are not available for %s/%s; see %s/releases", u.goos, u.goarch, repository)
	}
	current, err := parseVersion(u.current)
	if err != nil {
		return err
	}
	fmt.Fprintln(u.out, "Checking for the latest Hane release…")
	metadata, err := u.get(ctx, u.latestURL, 2<<20)
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("no published release is available yet; see %s/releases", repository)
	}
	if err != nil {
		return fmt.Errorf("check releases: %w", err)
	}
	var r release
	if err := json.Unmarshal(metadata, &r); err != nil {
		return errors.New("GitHub returned invalid release information")
	}
	latest, err := parseVersion(r.TagName)
	if err != nil || r.Draft || r.Prerelease {
		return errors.New("GitHub did not return a valid stable release")
	}
	if compareVersions(latest, current) <= 0 {
		fmt.Fprintf(u.out, "Hane %s is already up to date.\n", u.current)
		return nil
	}
	name := fmt.Sprintf("hane_%s_%s_%s.tar.gz", strings.TrimPrefix(r.TagName, "v"), u.goos, u.goarch)
	archiveURL, err := u.assetURL(r, name)
	if err != nil {
		return err
	}
	checksumURL, err := u.assetURL(r, "checksums.txt")
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(u.executable)
	if err != nil {
		return fmt.Errorf("locate installed binary: %w", err)
	}
	lock := flock.New(target + ".update.lock")
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("cannot update %s; install Hane in a directory you can write to: %w", target, err)
	}
	if !locked {
		return errors.New("another Hane update is in progress")
	}
	defer lock.Unlock()
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("the installed Hane executable is not a regular file")
	}
	// A sibling temporary file ensures replacement is an atomic rename and
	// checks directory permissions before downloading anything large.
	staged, err := os.CreateTemp(filepath.Dir(target), ".hane-update-*")
	if err != nil {
		return fmt.Errorf("cannot update %s; install Hane in a directory you can write to: %w", target, err)
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	fmt.Fprintf(u.out, "Downloading Hane %s for %s/%s…\n", r.TagName, u.goos, u.goarch)
	checksums, err := u.get(ctx, checksumURL, 1<<20)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	want, err := checksumFor(checksums, name)
	if err != nil {
		return err
	}
	archive, err := u.get(ctx, archiveURL, maxArchiveSize)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	if got := sha256.Sum256(archive); !bytes.Equal(got[:], want) {
		return errors.New("download checksum does not match; the installed binary was kept")
	}
	if err := extractBinary(archive, staged); err != nil {
		return fmt.Errorf("unpack release: %w", err)
	}
	if err := staged.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	currentInfo, err := os.Stat(target)
	if err != nil || !os.SameFile(info, currentInfo) {
		return errors.New("the installed binary changed during the update; retry using the current hane command")
	}
	if err := os.Rename(staged.Name(), target); err != nil {
		return fmt.Errorf("replace installed binary: %w", err)
	}
	fmt.Fprintf(u.out, "Updated Hane %s → %s. Run hane to start it.\n", u.current, strings.TrimPrefix(r.TagName, "v"))
	return nil
}

func (u updater) assetURL(r release, name string) (string, error) {
	expected := u.downloadBase + "/" + r.TagName + "/" + name
	found := ""
	for _, a := range r.Assets {
		if a.Name == name {
			if a.URL != expected || found != "" {
				return "", fmt.Errorf("release asset %s has an unexpected download location or duplicate entry", name)
			}
			found = a.URL
		}
	}
	if found == "" {
		return "", fmt.Errorf("release %s is missing %s; try again when its binaries have finished publishing", r.TagName, name)
	}
	return found, nil
}

func (u updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hane/"+u.current)
	if url == u.latestURL {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, errors.New("GitHub refused the download or its rate limit was reached; try again later")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("download exceeds the allowed size")
	}
	return data, nil
}

func checksumFor(data []byte, name string) ([]byte, error) {
	var found []byte
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 || strings.TrimPrefix(parts[1], "*") != name {
			continue
		}
		digest, err := hex.DecodeString(parts[0])
		if err != nil || len(digest) != sha256.Size || found != nil {
			return nil, fmt.Errorf("release has an invalid or duplicate checksum for %s", name)
		}
		found = digest
	}
	if found == nil {
		return nil, fmt.Errorf("release is missing the checksum for %s", name)
	}
	return found, nil
}

func extractBinary(data []byte, out io.Writer) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	// Bound decompression, including entries that we skip. Only the exact
	// regular-file entry "hane" is copied; archive paths are never extracted.
	tr := tar.NewReader(io.LimitReader(gz, 2*maxBinarySize))
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			if !found {
				return errors.New("archive contains no hane binary")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if h.Name != "hane" {
			continue
		}
		if found || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > maxBinarySize {
			return errors.New("archive contains an invalid or duplicate hane binary")
		}
		if _, err := io.CopyN(out, tr, h.Size); err != nil {
			return err
		}
		found = true
	}
}

func parseVersion(s string) ([3]uint64, error) {
	var v [3]uint64
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) == 3 {
		for i, part := range parts {
			n, err := strconv.ParseUint(part, 10, 64)
			if err != nil || part == "" || (len(part) > 1 && part[0] == '0') {
				return v, fmt.Errorf("unsupported version %q; expected MAJOR.MINOR.PATCH", s)
			}
			v[i] = n
		}
		return v, nil
	}
	return v, fmt.Errorf("unsupported version %q; expected MAJOR.MINOR.PATCH", s)
}

func compareVersions(a, b [3]uint64) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
