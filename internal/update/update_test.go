package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

type archiveEntry struct {
	name, content string
	kind          byte
}

func archiveFixture(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0755, Typeflag: kind, Size: int64(len(e.content))}
		if kind == tar.TypeSymlink {
			h.Linkname, h.Size = "elsewhere", 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type fixture struct {
	u            updater
	r            release
	archive      []byte
	checksums    string
	status       map[string]int
	requests     []string
	beforeBinary func()
	output       bytes.Buffer
}

func newFixture(t *testing.T, platform ...string) *fixture {
	t.Helper()
	f := &fixture{archive: archiveFixture(t, archiveEntry{name: "hane", content: "new executable"}), status: map[string]int{}}
	f.r.TagName = "v0.5.0"
	goos, goarch := "linux", "amd64"
	if len(platform) == 2 {
		goos, goarch = platform[0], platform[1]
	}
	name := fmt.Sprintf("hane_0.5.0_%s_%s.tar.gz", goos, goarch)
	f.checksums = fmt.Sprintf("%x  %s\n", sha256.Sum256(f.archive), name)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.URL.Path)
		if code := f.status[r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			return
		}
		switch r.URL.Path {
		case "/latest":
			_ = json.NewEncoder(w).Encode(f.r)
		case "/download/v0.5.0/checksums.txt":
			_, _ = io.WriteString(w, f.checksums)
		case "/download/v0.5.0/" + name:
			if f.beforeBinary != nil {
				f.beforeBinary()
			}
			_, _ = w.Write(f.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	target := filepath.Join(t.TempDir(), "hane")
	if err := os.WriteFile(target, []byte("old executable"), 0755); err != nil {
		t.Fatal(err)
	}
	f.u = updater{client: s.Client(), latestURL: s.URL + "/latest", downloadBase: s.URL + "/download", executable: target, goos: goos, goarch: goarch, current: "0.4.3", out: &f.output}
	for _, asset := range []string{name, "checksums.txt"} {
		f.r.Assets = append(f.r.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{asset, f.u.downloadBase + "/v0.5.0/" + asset})
	}
	return f
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("binary = %q, error = %v; want %q", b, err, want)
	}
}

func TestUpdateReplacesBinaryAndPreservesSymlink(t *testing.T) {
	f := newFixture(t)
	target := f.u.executable
	link := filepath.Join(t.TempDir(), "hane")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f.u.executable = link
	if err := f.u.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, "new executable")
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("update replaced the symlink instead of its target")
	}
	info, err = os.Stat(target)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("updated binary lost its executable permissions")
	}
	staged, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".hane-update-*"))
	if len(staged) != 0 || !strings.Contains(f.output.String(), "Updated Hane 0.4.3 → 0.5.0") {
		t.Fatal("update did not clean up or report success")
	}
}

func TestUpdateSelectsMatchingPlatform(t *testing.T) {
	for _, platform := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}} {
		t.Run(platform[0]+"/"+platform[1], func(t *testing.T) {
			f := newFixture(t, platform[0], platform[1])
			if err := f.u.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertFile(t, f.u.executable, "new executable")
		})
	}
}

func TestUpdateFailuresKeepInstalledBinary(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*fixture)
		want string
	}{
		{"no release", func(f *fixture) { f.status["/latest"] = 404 }, "no published release"},
		{"rate limited", func(f *fixture) { f.status["/latest"] = 429 }, "rate limit"},
		{"download failure", func(f *fixture) { f.status["/download/v0.5.0/hane_0.5.0_linux_amd64.tar.gz"] = 500 }, "HTTP 500"},
		{"checksum mismatch", func(f *fixture) { f.archive = append(f.archive, 'x') }, "checksum does not match"},
		{"checksum missing", func(f *fixture) { f.checksums = "" }, "missing the checksum"},
		{"duplicate checksum", func(f *fixture) { f.checksums += f.checksums }, "duplicate checksum"},
		{"asset missing", func(f *fixture) { f.r.Assets = nil }, "missing hane_"},
		{"foreign asset", func(f *fixture) { f.r.Assets[0].URL = "https://example.com/hane.tar.gz" }, "unexpected download"},
		{"draft", func(f *fixture) { f.r.Draft = true }, "valid stable release"},
		{"prerelease", func(f *fixture) { f.r.Prerelease = true }, "valid stable release"},
		{"invalid version", func(f *fixture) { f.r.TagName = "next" }, "valid stable release"},
		{"invalid archive", func(f *fixture) {
			f.archive = []byte("not a gzip file")
			f.checksums = fmt.Sprintf("%x  hane_0.5.0_linux_amd64.tar.gz\n", sha256.Sum256(f.archive))
		}, "unpack release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.edit(f)
			err := f.u.run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %s", err, tc.want)
			}
			assertFile(t, f.u.executable, "old executable")
			staged, _ := filepath.Glob(filepath.Join(filepath.Dir(f.u.executable), ".hane-update-*"))
			if len(staged) > 0 {
				t.Fatal("failed update left a temporary binary")
			}
		})
	}
}

func TestUpdateSkipsCurrentOrOlderRelease(t *testing.T) {
	for _, current := range []string{"0.5.0", "0.6.0", "0.10.0", "1.0.0"} {
		t.Run(current, func(t *testing.T) {
			f := newFixture(t)
			f.u.current = current
			if err := f.u.run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(f.requests) != 1 || !strings.Contains(f.output.String(), "already up to date") {
				t.Fatal("updater should not download assets or downgrade")
			}
			assertFile(t, f.u.executable, "old executable")
		})
	}
}

func TestUpdateRefusesConcurrentReplacement(t *testing.T) {
	f := newFixture(t)
	lock := flock.New(f.u.executable + ".update.lock")
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatal("cannot lock fixture")
	}
	defer lock.Unlock()
	if err := f.u.run(context.Background()); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("expected concurrent-update error, got %v", err)
	}
	assertFile(t, f.u.executable, "old executable")
}

func TestUpdateDoesNotOverwriteAnExternalInstall(t *testing.T) {
	f := newFixture(t)
	f.beforeBinary = func() {
		other := f.u.executable + ".external"
		if err := os.WriteFile(other, []byte("external install"), 0755); err != nil {
			panic(err)
		}
		if err := os.Rename(other, f.u.executable); err != nil {
			panic(err)
		}
	}
	if err := f.u.run(context.Background()); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatalf("expected concurrent-install error, got %v", err)
	}
	assertFile(t, f.u.executable, "external install")
}

func TestUpdateRejectsUnsupportedPlatformAndCancellation(t *testing.T) {
	f := newFixture(t)
	f.u.goos = "windows"
	if err := f.u.run(context.Background()); err == nil || len(f.requests) != 0 {
		t.Fatal("unsupported platform should fail before downloading")
	}
	f.u.goos = "linux"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.u.run(ctx); err == nil {
		t.Fatal("canceled update succeeded")
	}
	assertFile(t, f.u.executable, "old executable")
}

func TestUpdateCancellationDuringDownloadKeepsOldBinary(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.beforeBinary = cancel
	if err := f.u.run(ctx); err == nil {
		t.Fatal("canceled update replaced the binary")
	}
	assertFile(t, f.u.executable, "old executable")
}

func TestUpdateUnwritableInstallKeepsOldBinary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into directories without write permission")
	}
	f := newFixture(t)
	dir := filepath.Dir(f.u.executable)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	if err := f.u.run(context.Background()); err == nil || !strings.Contains(err.Error(), "directory you can write") {
		t.Fatalf("expected permission error, got %v", err)
	}
	assertFile(t, f.u.executable, "old executable")
}

func TestDownloadSizeIsBounded(t *testing.T) {
	f := newFixture(t)
	if _, err := f.u.get(context.Background(), f.u.latestURL, 8); err == nil || !strings.Contains(err.Error(), "allowed size") {
		t.Fatalf("oversized response accepted: %v", err)
	}
}

func TestArchiveCannotInstallLinksPathsOrDuplicateBinaries(t *testing.T) {
	for _, entries := range [][]archiveEntry{
		{{name: "../hane", content: "escape"}},
		{{name: "hane", kind: tar.TypeSymlink}},
		{{name: "hane", content: "first"}, {name: "hane", content: "second"}},
		{{name: "hane"}},
	} {
		if err := extractBinary(archiveFixture(t, entries...), io.Discard); err == nil {
			t.Fatal("unsafe or invalid archive was accepted")
		}
	}
}
