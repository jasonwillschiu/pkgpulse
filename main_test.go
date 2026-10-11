package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

/* ---- Parsers ---- */

func TestParseAPKDB(t *testing.T) {
	db := strings.Join([]string{
		"P:musl", "V:1.2.5-r0", "S:400000", "I:700000", "",
		"P:tiny", "V:1", "I:512", "", // sub-KB packages keep their size
		"S:2048", "P:no-installed-size", "V:2", "", // S: is only a fallback
		"P:last", "V:3", "I:4096", // no trailing blank line
	}, "\n")

	got := parseAPKDB([]byte(db))
	want := []pkg{
		{Name: "musl", Version: "1.2.5-r0", SizeBytes: 700000, Type: "apk"},
		{Name: "tiny", Version: "1", SizeBytes: 512, Type: "apk"},
		{Name: "no-installed-size", Version: "2", SizeBytes: 2048, Type: "apk"},
		{Name: "last", Version: "3", SizeBytes: 4096, Type: "apk"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseAPKDB:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseDpkgDBStatus(t *testing.T) {
	entry := func(name, status string) string {
		s := "Package: " + name + "\nVersion: 1.0\nInstalled-Size: 10\n"
		if status != "" {
			s += "Status: " + status + "\n"
		}
		return s + "Description: test\n continuation line\n\n"
	}
	db := entry("installed", "install ok installed") +
		entry("held", "hold ok installed") +
		entry("unpacked", "install ok unpacked") +
		entry("removed", "deinstall ok config-files") +
		entry("purged", "purge ok not-installed") +
		entry("broken", "install reinstreq half-installed") +
		entry("no-status", "")

	names := func(pkgs []pkg) []string {
		var out []string
		for _, p := range pkgs {
			out = append(out, p.Name)
		}
		return out
	}

	got := names(parseDpkgDB([]byte(db), false))
	if want := []string{"installed", "held", "unpacked"}; !slices.Equal(got, want) {
		t.Errorf("parseDpkgDB(assumeInstalled=false) = %v, want %v", got, want)
	}
	got = names(parseDpkgDB([]byte(db), true))
	if want := []string{"installed", "held", "unpacked", "no-status"}; !slices.Equal(got, want) {
		t.Errorf("parseDpkgDB(assumeInstalled=true) = %v, want %v", got, want)
	}

	pkgs := parseDpkgDB([]byte(entry("kb", "install ok installed")), false)
	if len(pkgs) != 1 || pkgs[0].SizeBytes != 10*1024 || pkgs[0].Type != "deb" {
		t.Errorf("Installed-Size should be converted from KiB to bytes, got %+v", pkgs)
	}
}

func TestParseDpkgDBLongLines(t *testing.T) {
	// Lines beyond bufio.Scanner's 64 KiB default must not truncate parsing
	db := "Package: big\nStatus: install ok installed\nInstalled-Size: 1\nDescription: " +
		strings.Repeat("x", 200_000) + "\n\nPackage: after\nStatus: install ok installed\nInstalled-Size: 1\n"
	pkgs := parseDpkgDB([]byte(db), false)
	if len(pkgs) != 2 || pkgs[1].Name != "after" {
		t.Fatalf("expected both packages, got %+v", pkgs)
	}
}

func TestCombineDpkgStatusParts(t *testing.T) {
	parts := map[string][]byte{
		"var/lib/dpkg/status.d/b": []byte("Package: b\nInstalled-Size: 1"),
		"var/lib/dpkg/status.d/a": []byte("Package: a\nInstalled-Size: 1\n"),
		"var/lib/dpkg/status.d/c": nil,
	}
	got := string(combineDpkgStatusParts(parts))
	want := "Package: a\nInstalled-Size: 1\n\nPackage: b\nInstalled-Size: 1\n\n"
	if got != want {
		t.Fatalf("combineDpkgStatusParts = %q, want %q", got, want)
	}
}

/* ---- Rows and comparison ---- */

func TestBuildRows(t *testing.T) {
	rows := buildRows([]pkg{
		{Name: "libc6", Version: "2.36", SizeBytes: 100}, // multi-arch duplicates merge
		{Name: "libc6", Version: "2.36", SizeBytes: 50},
		{Name: "kernel", Version: "6.1", SizeBytes: 10}, // multiple versions merge
		{Name: "kernel", Version: "6.2", SizeBytes: 20},
		{Name: "zeta", Version: "1", SizeBytes: 30}, // ties sort by name
		{Name: "alpha", Version: "1", SizeBytes: 30},
		{Name: "gpg-pubkey", Version: "x", SizeBytes: 0}, // zero size dropped
	})
	want := []row{
		{Name: "libc6", Ver: "2.36", Bytes: 150},
		{Name: "alpha", Ver: "1", Bytes: 30},
		{Name: "kernel", Ver: "6.1, 6.2", Bytes: 30},
		{Name: "zeta", Ver: "1", Bytes: 30},
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("buildRows:\n got %+v\nwant %+v", rows, want)
	}
}

func TestComparisonPackages(t *testing.T) {
	results := []imageResult{
		{Rows: []row{{Name: "b", Bytes: 5}, {Name: "a", Bytes: 1}}},
		{Rows: []row{{Name: "c", Bytes: 9}, {Name: "a", Bytes: 7}}},
	}
	if got := comparisonPackages(results, 0); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("all packages = %v", got)
	}
	if got := comparisonPackages(results, 2); !slices.Equal(got, []string{"c", "a"}) {
		t.Errorf("top 2 by max size = %v", got)
	}
}

func TestBuildCSVImageColumns(t *testing.T) {
	results := []imageResult{{Image: "Alpine:3.20"}, {Image: "alpine/3.20"}, {Image: "!!!"}}
	cols := buildCSVImageColumns(results)
	if cols[0] != "alpine_3_20" || !strings.HasPrefix(cols[1], "alpine_3_20_") || cols[2] != "image" {
		t.Fatalf("buildCSVImageColumns = %v", cols)
	}
}

func TestTruncIsRuneSafe(t *testing.T) {
	if got := trunc("héllo wörld", 6); got != "héllo…" {
		t.Fatalf("trunc = %q", got)
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	results := []imageResult{
		{Image: "a", Rows: []row{{Name: "x", Ver: "1", Bytes: 1536}}, InstalledBytes: 1536},
		{Image: "b", Err: io.ErrUnexpectedEOF},
	}
	if err := writeJSON(&buf, results); err != nil {
		t.Fatal(err)
	}
	var report jsonReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Images) != 2 || report.Images[0].PackageCount != 1 ||
		report.Images[0].Packages[0].InstalledBytes != 1536 || report.Images[1].Error == "" {
		t.Fatalf("unexpected report: %+v", report)
	}
}

/* ---- CLI ---- */

func TestParseArgs(t *testing.T) {
	opts, err := parseArgs([]string{"alpine", "--csv", "out.csv", "--platform=linux/arm64", "debian", "--json",
		"--top", "5", "--jobs=2", "alpine", "--no-cache", "--use-syft"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opts.images, []string{"alpine", "debian"}) || opts.csvPath != "out.csv" ||
		opts.platform != "linux/arm64" || !opts.jsonOut || opts.top != 5 || opts.jobs != 2 ||
		!opts.noCache || !opts.useSyft {
		t.Fatalf("unexpected options: %+v", opts)
	}

	defaults, err := parseArgs([]string{"alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.platform != defaultPlatform || defaults.jobs != defaultConcurrency {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	for _, args := range [][]string{
		{},
		{"--foo", "alpine"},
		{"alpine", "--csv"},
		{"alpine", "--csv", "--json"},
		{"alpine", "--csv="},
		{"alpine", "--top", "-1"},
		{"alpine", "--jobs", "0"},
		{"alpine", "--platform", "arm64"},
		{"alpine", "--json=true"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q) succeeded, want error", args)
		}
	}
}

func TestNextFreePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pkgpulse.csv")
	if got := nextFreePath(p); got != p {
		t.Fatalf("nextFreePath = %s, want %s", got, p)
	}
	for _, f := range []string{"pkgpulse.csv", "pkgpulse-2.csv"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := nextFreePath(p), filepath.Join(dir, "pkgpulse-3.csv"); got != want {
		t.Fatalf("nextFreePath = %s, want %s", got, want)
	}
}

/* ---- Cache ---- */

func TestCacheRoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	short, err := normalizeRef("alpine")
	if err != nil {
		t.Fatal(err)
	}
	long, err := normalizeRef("docker.io/library/alpine:latest")
	if err != nil {
		t.Fatal(err)
	}
	if short != long {
		t.Fatalf("references should normalize to the same key: %s vs %s", short, long)
	}

	img, err := random.Image(256, 2)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference("alpine")
	if err != nil {
		t.Fatal(err)
	}
	key := cacheKey{ref: short, platform: "linux/amd64"}
	if _, _, err := loadFromCache(key); err == nil {
		t.Fatal("expected a cache miss")
	}
	if err := saveToCache(key, ref, img, cacheEntry{ImageRef: short, Platform: key.platform, CompressedBytes: 42}); err != nil {
		t.Fatal(err)
	}

	cached, entry, err := loadFromCache(key)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, _ := img.Digest()
	gotDigest, _ := cached.Digest()
	if gotDigest != wantDigest || entry.CompressedBytes != 42 || entry.Digest != wantDigest.String() {
		t.Fatalf("round trip mismatch: digest %s vs %s, entry %+v", gotDigest, wantDigest, entry)
	}

	// Other platforms get their own entry
	if _, _, err := loadFromCache(cacheKey{ref: short, platform: "linux/arm64"}); err == nil {
		t.Fatal("expected a cache miss for another platform")
	}

	items, err := listCache()
	if err != nil || len(items) != 1 {
		t.Fatalf("listCache = %v, %v", items, err)
	}
	if n, err := removeCacheEntries("alpine:latest"); err != nil || n != 1 {
		t.Fatalf("removeCacheEntries = %d, %v", n, err)
	}
	if items, _ := listCache(); len(items) != 0 {
		t.Fatalf("cache not empty after rm: %v", items)
	}
	leftovers, _ := filepath.Glob(filepath.Join(getCacheDir(), "*"))
	if len(leftovers) != 0 {
		t.Fatalf("unexpected files left in cache dir: %v", leftovers)
	}
}

/* ---- Layer walking (overlay semantics) ---- */

type tarEntry struct {
	name, body, link string
	mode             int64
	typ              byte
}

func file(name, body string) tarEntry {
	return tarEntry{name: name, body: body, mode: 0644, typ: tar.TypeReg}
}
func exe(name, body string) tarEntry {
	return tarEntry{name: name, body: body, mode: 0755, typ: tar.TypeReg}
}
func dir(name string) tarEntry { return tarEntry{name: name, mode: 0755, typ: tar.TypeDir} }
func symlink(name, target string) tarEntry {
	return tarEntry{name: name, link: target, mode: 0777, typ: tar.TypeSymlink}
}
func whiteout(p string) tarEntry {
	return file(path.Join(path.Dir(p), ".wh."+path.Base(p)), "")
}
func opaque(dir string) tarEntry { return file(path.Join(dir, ".wh..wh..opq"), "") }

func testImage(t *testing.T, layers ...[]tarEntry) v1.Image {
	t.Helper()
	img := empty.Image
	for _, entries := range layers {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, e := range entries {
			hdr := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: e.typ, Linkname: e.link, Size: int64(len(e.body))}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()
		layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if img, err = mutate.AppendLayers(img, layer); err != nil {
			t.Fatal(err)
		}
	}
	return img
}

func packageNames(t *testing.T, img v1.Image) []string {
	t.Helper()
	pkgs, warnings, err := extractPackagesFromImage(img, func(string, int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	var names []string
	for _, p := range pkgs {
		names = append(names, p.Name+"="+p.Version)
	}
	slices.Sort(names)
	return names
}

const (
	apkDB  = "P:musl\nV:1.2\nI:1024\n\nP:busybox\nV:1.36\nI:2048\n"
	apkDB2 = "P:musl\nV:1.3\nI:1024\n"
	dpkgDB = "Package: libc6\nVersion: 2.36\nStatus: install ok installed\nInstalled-Size: 10\n"
)

func TestOverlaySemantics(t *testing.T) {
	busybox := "\x7fELF... BusyBox v1.36.1 (2024-01-01) multi-call binary"

	tests := []struct {
		name   string
		layers [][]tarEntry
		want   []string
	}{
		{
			name:   "later layer replaces database",
			layers: [][]tarEntry{{file("lib/apk/db/installed", apkDB)}, {file("lib/apk/db/installed", apkDB2)}},
			want:   []string{"musl=1.3"},
		},
		{
			name:   "usr-merged apk database (wolfi)",
			layers: [][]tarEntry{{file("usr/lib/apk/db/installed", apkDB2)}},
			want:   []string{"musl=1.3"},
		},
		{
			name:   "tar paths with ./ prefix",
			layers: [][]tarEntry{{file("./var/lib/dpkg/status", dpkgDB)}},
			want:   []string{"libc6=2.36"},
		},
		{
			name: "file whiteout removes database",
			layers: [][]tarEntry{
				{file("var/lib/dpkg/status", dpkgDB), file("lib/apk/db/installed", apkDB2)},
				{whiteout("var/lib/dpkg/status")},
			},
			want: []string{"musl=1.3"},
		},
		{
			name: "directory whiteout removes database",
			layers: [][]tarEntry{
				{file("var/lib/dpkg/status", dpkgDB), file("lib/apk/db/installed", apkDB2)},
				{whiteout("var/lib/dpkg")},
			},
			want: []string{"musl=1.3"},
		},
		{
			name: "whiteout of status.d directory removes fragments",
			layers: [][]tarEntry{
				{file("var/lib/dpkg/status.d/libc6", dpkgDB), file("lib/apk/db/installed", apkDB2)},
				{whiteout("var/lib/dpkg/status.d")},
			},
			want: []string{"musl=1.3"},
		},
		{
			name: "status.d fragments without Status are installed",
			layers: [][]tarEntry{{
				file("var/lib/dpkg/status.d/base", "Package: base-files\nVersion: 12\nInstalled-Size: 4\n"),
				file("var/lib/dpkg/status.d/base.md5sums", "ignored"),
			}},
			want: []string{"base-files=12"},
		},
		{
			name: "opaque directory hides lower entries but keeps same-layer ones",
			layers: [][]tarEntry{
				{file("var/lib/dpkg/status.d/old", "Package: old\nVersion: 1\nInstalled-Size: 4\n")},
				{file("var/lib/dpkg/status.d/new", "Package: new\nVersion: 2\nInstalled-Size: 4\n"), opaque("var/lib/dpkg/status.d")},
			},
			want: []string{"new=2"},
		},
		{
			name: "symlink replacing a directory hides its lower contents",
			layers: [][]tarEntry{
				{file("lib/apk/db/installed", apkDB), file("usr/lib/apk/db/installed", apkDB2)},
				{symlink("lib", "usr/lib")},
			},
			want: []string{"musl=1.3"},
		},
		{
			name: "directory entry in a later layer merges",
			layers: [][]tarEntry{
				{file("lib/apk/db/installed", apkDB2)},
				{dir("lib"), dir("lib/apk"), dir("lib/apk/db")},
			},
			want: []string{"musl=1.3"},
		},
		{
			name:   "binary-only image detects busybox",
			layers: [][]tarEntry{{exe("bin/busybox", busybox), exe("bin/sh", busybox), file("bin/readme", "not executable")}},
			want:   []string{"busybox=1.36.1"},
		},
		{
			name: "whited-out binary is not reported",
			layers: [][]tarEntry{
				{exe("usr/bin/tool", "plain"), exe("usr/bin/other", "plain")},
				{whiteout("usr/bin/tool")},
			},
			want: []string{"other=-"},
		},
		{
			name: "binaries added after a later-deleted database are inspected",
			layers: [][]tarEntry{
				{file("lib/apk/db/installed", apkDB)},
				{exe("bin/busybox", busybox)},
				{whiteout("lib/apk/db/installed")},
			},
			want: []string{"busybox=1.36.1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := packageNames(t, testImage(t, tt.layers...))
			if !slices.Equal(got, tt.want) {
				t.Fatalf("packages = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectsGoBinary(t *testing.T) {
	// The test binary itself is a Go binary with embedded build info
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	pkgs, _, err := extractPackagesFromImage(testImage(t, []tarEntry{exe("usr/local/bin/app", string(data))}), func(string, int64, int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].Name != "app" || !strings.HasPrefix(pkgs[0].Version, "go1.") ||
		pkgs[0].SizeBytes != int64(len(data)) {
		t.Fatalf("unexpected packages: %+v", pkgs)
	}
}
