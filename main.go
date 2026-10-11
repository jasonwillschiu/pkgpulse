package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/glebarez/go-sqlite" // SQLite driver for RPM DB
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	rpmdb "github.com/knqyf263/go-rpmdb/pkg"
)

// version is injected at build time (-ldflags "-X main.version=...").
// When unset, the module version recorded by `go install` is used.
var version string

func appVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

const (
	// Default concurrency limit for parallel image analysis
	defaultConcurrency = 5
	// Platform selected from multi-arch images unless --platform is given
	defaultPlatform = "linux/amd64"
	// Catalogers to use for syft fallback (skip language-specific ones for speed)
	defaultCatalogers = "apk,dpkg,rpm,binary"
	// How long to wait for the registry when checking whether a cached tag moved
	freshnessTimeout = 10 * time.Second
	// Cached tags fetched or verified within this window are used without a registry check
	cacheRecheckAfter = time.Hour
	// File name used when CSV is exported automatically for large comparisons
	autoCSVName = "pkgpulse.csv"
)

// Package database paths, in priority order. Newer distros moved these under
// usr/ and keep a compatibility symlink at the old location.
var apkDBPaths = []string{
	"usr/lib/apk/db/installed", // wolfi / chainguard (usr-merged)
	"lib/apk/db/installed",
}

const (
	dpkgDBPath    = "var/lib/dpkg/status"
	dpkgStatusDir = "var/lib/dpkg/status.d" // split metadata used by distroless
)

type rpmDBLocation struct{ path, format string }

var rpmDBPaths = []rpmDBLocation{
	{"usr/lib/sysimage/rpm/rpmdb.sqlite", "sqlite"}, // fedora 36+, rhel 10
	{"var/lib/rpm/rpmdb.sqlite", "sqlite"},
	{"usr/lib/sysimage/rpm/Packages.db", "ndb"}, // opensuse
	{"var/lib/rpm/Packages.db", "ndb"},
	{"usr/lib/sysimage/rpm/Packages", "bdb"},
	{"var/lib/rpm/Packages", "bdb"},
}

// Directories scanned for standalone binaries when an image has no package database
var binaryDirs = map[string]bool{
	"bin": true, "sbin": true, "usr/bin": true, "usr/sbin": true, "usr/local/bin": true,
}

// dpkg package states whose files are present on disk
var dpkgStatesOnDisk = map[string]bool{
	"installed": true, "unpacked": true, "half-configured": true,
	"triggers-awaited": true, "triggers-pending": true,
}

var busyBoxVersionRe = regexp.MustCompile(`BusyBox v([0-9][0-9A-Za-z.+~:_-]*)`)
var debianGLIBCVersionRe = regexp.MustCompile(`\(Debian GLIBC ([^)]+)\)`)
var glibcSymbolVersionRe = regexp.MustCompile(`GLIBC_([0-9]+(?:\.[0-9]+){1,2})`)

/* ---- CLI options ---- */

type options struct {
	images   []string
	csvPath  string
	useSyft  bool
	noCache  bool
	jsonOut  bool
	platform string
	top      int
	jobs     int
}

// parseArgs parses flags and image references. Flags may appear anywhere and
// accept both "--flag value" and "--flag=value" forms.
func parseArgs(args []string) (options, error) {
	opts := options{platform: defaultPlatform, jobs: defaultConcurrency}
	seen := make(map[string]bool)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		flagName, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if k, v, ok := strings.Cut(arg, "="); ok {
				flagName, value, hasValue = k, v, true
			}
		}
		takeValue := func() (string, error) {
			if hasValue {
				if value == "" {
					return "", fmt.Errorf("%s requires a value", flagName)
				}
				return value, nil
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", fmt.Errorf("%s requires a value", flagName)
			}
			i++
			return args[i], nil
		}
		takeInt := func(minValue int) (int, error) {
			s, err := takeValue()
			if err != nil {
				return 0, err
			}
			n, err := strconv.Atoi(s)
			if err != nil || n < minValue {
				return 0, fmt.Errorf("%s must be an integer >= %d, got %q", flagName, minValue, s)
			}
			return n, nil
		}

		var err error
		switch flagName {
		case "--csv":
			opts.csvPath, err = takeValue()
		case "--platform":
			var s string
			if s, err = takeValue(); err == nil {
				opts.platform, err = normalizePlatform(s)
			}
		case "--top":
			opts.top, err = takeInt(0)
		case "--jobs":
			opts.jobs, err = takeInt(1)
		case "--use-syft", "--no-cache", "--json":
			if hasValue {
				return opts, fmt.Errorf("%s does not take a value", flagName)
			}
			switch flagName {
			case "--use-syft":
				opts.useSyft = true
			case "--no-cache":
				opts.noCache = true
			case "--json":
				opts.jsonOut = true
			}
		case "--version", "-v", "--help", "-h":
			// Handled before parsing
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag: %s", arg)
			}
			if !seen[arg] {
				seen[arg] = true
				opts.images = append(opts.images, arg)
			}
		}
		if err != nil {
			return opts, err
		}
	}

	if len(opts.images) == 0 {
		return opts, errors.New("no images specified")
	}
	return opts, nil
}

func normalizePlatform(s string) (string, error) {
	p, err := v1.ParsePlatform(s)
	if err != nil {
		return "", fmt.Errorf("invalid --platform: %w", err)
	}
	if p.OS == "" || p.Architecture == "" {
		return "", fmt.Errorf("invalid --platform %q: expected os/arch[/variant], e.g. linux/arm64", s)
	}
	return p.String(), nil
}

/* ---- Cache types ---- */

// Cache metadata stored alongside tarball
type cacheEntry struct {
	ImageRef        string    `json:"image_ref"`
	Platform        string    `json:"platform,omitempty"`
	Digest          string    `json:"digest"`               // platform image digest
	RefDigest       string    `json:"ref_digest,omitempty"` // digest the reference resolved to; detects moved tags
	CompressedBytes int64     `json:"compressed_bytes,omitempty"`
	CachedAt        time.Time `json:"cached_at"`
	CheckedAt       time.Time `json:"checked_at,omitzero"` // last time the tag was confirmed unchanged
	SizeBytes       int64     `json:"size_bytes"`
}

type cacheKey struct {
	ref      string // fully qualified reference, e.g. index.docker.io/library/alpine:latest
	platform string
}

/* ---- Native package representation ---- */
type pkg struct {
	Name      string
	Version   string
	SizeBytes int64
	Type      string // "apk", "deb", "rpm", "binary"
}

/* ---- Minimal Syft JSON we need (syft-json schema) - for fallback ---- */
type syftSBOM struct {
	Artifacts             []syftArtifact     `json:"artifacts"`
	ArtifactRelationships []syftRelationship `json:"artifactRelationships"`
	Files                 []syftFile         `json:"files"`
}
type syftArtifact struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Type     string       `json:"type"`
	Metadata syftMetadata `json:"metadata"`
}
type syftMetadata struct {
	InstalledSize int64 `json:"installedSize"` // KB for deb
	Size          int64 `json:"size"`          // bytes for rpm and apk
}
type syftRelationship struct {
	Parent string `json:"parent"`
	Child  string `json:"child"`
	Type   string `json:"type"`
}
type syftFile struct {
	ID       string           `json:"id"`
	Metadata syftFileMetadata `json:"metadata"`
}
type syftFileMetadata struct {
	Size int64 `json:"size"`
}

/* ---- Package row for output ---- */
type row struct {
	Name, Ver string
	Bytes     int64
}

func (r row) MB() float64 { return toMB(r.Bytes) }

type imageResult struct {
	Image           string
	Platform        string
	Source          string // "cache" (loaded from local cache) or "registry" (fetched this run)
	CompressedBytes int64
	InstalledBytes  int64
	Rows            []row
	PackageMap      map[string]row
	Warnings        []string
	Err             error
}

/* ---- Progress rendering ---- */

type progressEvent struct {
	idx       int
	image     string
	stage     string
	message   string
	current   int64
	totalSize int64
	rateBps   float64
}

type imageProgressState struct {
	image     string
	stage     string
	message   string
	current   int64
	totalSize int64
	rateBps   float64
	done      bool
}

type progressReadCloser struct {
	io.ReadCloser
	onRead func(int)
}

func (p *progressReadCloser) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	if n > 0 && p.onRead != nil {
		p.onRead(n)
	}
	return n, err
}

type progressTransport struct {
	base    http.RoundTripper
	onBytes func(int64)
}

func (t *progressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.Body == nil || t.onBytes == nil {
		return resp, nil
	}
	if req.Method != http.MethodGet {
		return resp, nil
	}
	resp.Body = &progressReadCloser{
		ReadCloser: resp.Body,
		onRead: func(n int) {
			t.onBytes(int64(n))
		},
	}
	return resp, nil
}

func stageLabel(stage string) string {
	switch stage {
	case "resolving":
		return "resolving image"
	case "cache_load":
		return "loading cache"
	case "cache_check":
		return "checking for updates"
	case "manifest":
		return "fetching manifest"
	case "downloading":
		return "downloading"
	case "cache_save":
		return "saving cache"
	case "cache_reload":
		return "reloading cache"
	case "parsing":
		return "parsing packages"
	case "syft":
		return "running syft"
	case "processing":
		return "processing output"
	case "done":
		return "done"
	case "failed":
		return "failed"
	default:
		return stage
	}
}

func isFinalStage(stage string) bool { return stage == "done" || stage == "failed" }

func buildProgressLine(idx, total, done int, s imageProgressState) string {
	line := fmt.Sprintf("[%d/%d] %s | %s", idx+1, total, trunc(s.image, 28), stageLabel(s.stage))

	if s.stage == "downloading" {
		currentMB := toMB(s.current)
		if s.totalSize > 0 {
			line += fmt.Sprintf(" %.1f/%.1f MB", currentMB, toMB(s.totalSize))
		} else {
			line += fmt.Sprintf(" %.1f MB", currentMB)
		}
		if s.rateBps > 0 {
			line += fmt.Sprintf(" (%.1f MB/s)", s.rateBps/(1024.0*1024.0))
		}
	} else if s.totalSize > 0 {
		line += fmt.Sprintf(" %d/%d", s.current, s.totalSize)
	}

	if s.message != "" && s.stage != "downloading" {
		line += " - " + trunc(s.message, 40)
	}
	if s.done && s.stage != "failed" {
		line += " | done"
	}
	line += fmt.Sprintf(" | overall %d/%d", done, total)
	return line
}

// runProgressRenderer draws live per-image status lines on an interactive
// terminal, or prints one plain line per stage change otherwise (CI, pipes).
func runProgressRenderer(progressChan <-chan progressEvent, images []string, interactive bool, done chan<- struct{}) {
	defer close(done)
	total := len(images)

	if !interactive {
		lastStage := make([]string, total)
		for ev := range progressChan {
			if ev.idx < 0 || ev.idx >= total || ev.stage == lastStage[ev.idx] || isFinalStage(ev.stage) {
				continue
			}
			lastStage[ev.idx] = ev.stage
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %s\n", ev.idx+1, total, ev.image, stageLabel(ev.stage))
		}
		return
	}

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	states := make([]imageProgressState, total)
	for i := range states {
		states[i] = imageProgressState{image: images[i], stage: "queued"}
	}

	printed := false
	render := func() {
		doneCount := 0
		for _, s := range states {
			if s.done {
				doneCount++
			}
		}

		if printed && total > 0 {
			fmt.Fprintf(os.Stderr, "\033[%dA", total)
		}

		for i := range states {
			line := buildProgressLine(i, total, doneCount, states[i])
			fmt.Fprintf(os.Stderr, "\033[2K\r%s\n", line)
		}
		printed = true
	}

	for {
		select {
		case ev, ok := <-progressChan:
			if !ok {
				render()
				return
			}
			if ev.idx < 0 || ev.idx >= total {
				continue
			}
			states[ev.idx] = imageProgressState{
				image:     ev.image,
				stage:     ev.stage,
				message:   ev.message,
				current:   ev.current,
				totalSize: ev.totalSize,
				rateBps:   ev.rateBps,
				done:      isFinalStage(ev.stage),
			}
			render()
		case <-ticker.C:
			render()
		}
	}
}

// startDownloadProgress emits periodic byte/throughput updates until stopped.
func startDownloadProgress(downloaded, expected *atomic.Int64, emit func(stage, message string, current, totalSize int64, rateBps float64)) (stop func()) {
	stopCh := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		defer close(stopped)
		lastBytes := int64(0)
		lastTick := time.Now()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				current := downloaded.Load()
				now := time.Now()
				rate := 0.0
				if dt := now.Sub(lastTick).Seconds(); dt > 0 {
					rate = float64(current-lastBytes) / dt
				}
				lastBytes = current
				lastTick = now
				if current > 0 {
					emit("downloading", "pulling image bytes", current, expected.Load(), rate)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			<-stopped
		})
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

/* ---- Cache functions ---- */

func getCacheDir() string {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cacheDir = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheDir, "pkgpulse")
}

// normalizeRef returns the fully qualified form of an image reference so that
// "alpine" and "docker.io/library/alpine:latest" share a cache entry.
func normalizeRef(image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", err
	}
	return ref.Name(), nil
}

// displayRef shortens Docker Hub references for display.
func displayRef(ref string) string {
	if s, ok := strings.CutPrefix(ref, "index.docker.io/library/"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(ref, "index.docker.io/"); ok {
		return s
	}
	return ref
}

func sanitizeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

func getCachePaths(key cacheKey) (tarPath, metaPath string) {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return "", ""
	}
	baseName := fmt.Sprintf("%s_%s_%s",
		sanitizeFileName(displayRef(key.ref)),
		sanitizeFileName(key.platform),
		shortHash(key.ref+"|"+key.platform, 16))
	return filepath.Join(cacheDir, baseName+".tar"), filepath.Join(cacheDir, baseName+".json")
}

// loadFromCache returns the cached image and its metadata. A missing entry
// yields an error wrapping fs.ErrNotExist.
func loadFromCache(key cacheKey) (v1.Image, *cacheEntry, error) {
	tarPath, metaPath := getCachePaths(key)
	if tarPath == "" {
		return nil, nil, fmt.Errorf("could not determine cache directory: %w", fs.ErrNotExist)
	}

	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(tarPath); err != nil {
		return nil, nil, err
	}
	var entry cacheEntry
	if err := json.Unmarshal(metaData, &entry); err != nil {
		return nil, nil, fmt.Errorf("parse cache metadata: %w", err)
	}

	img, err := tarball.ImageFromPath(tarPath, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("read cache tarball: %w", err)
	}
	return img, &entry, nil
}

// writeFileAtomic writes via a temp file and rename so readers and concurrent
// writers never observe a partial file.
func writeFileAtomic(dst string, write func(w io.Writer) error) (size int64, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if err = write(tmp); err != nil {
		return 0, err
	}
	info, err := tmp.Stat()
	if err != nil {
		return 0, err
	}
	if err = tmp.Close(); err != nil {
		return 0, err
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func saveToCache(key cacheKey, ref name.Reference, img v1.Image, entry cacheEntry) error {
	tarPath, metaPath := getCachePaths(key)
	if tarPath == "" {
		return errors.New("could not determine cache directory")
	}
	if err := os.MkdirAll(filepath.Dir(tarPath), 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	digest, err := img.Digest()
	if err != nil {
		return fmt.Errorf("get digest: %w", err)
	}

	size, err := writeFileAtomic(tarPath, func(w io.Writer) error {
		return tarball.Write(ref, img, w)
	})
	if err != nil {
		return fmt.Errorf("write tarball: %w", err)
	}

	entry.Digest = digest.String()
	entry.CachedAt = time.Now()
	entry.SizeBytes = size
	if err := writeCacheMeta(metaPath, entry); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

func writeCacheMeta(metaPath string, entry cacheEntry) error {
	metaData, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeFileAtomic(metaPath, func(w io.Writer) error {
		_, err := w.Write(metaData)
		return err
	})
	return err
}

type cachedItem struct {
	entry    cacheEntry
	metaPath string
}

func listCache() ([]cachedItem, error) {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return nil, errors.New("could not determine cache directory")
	}

	entries, err := os.ReadDir(cacheDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cached []cachedItem
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		metaPath := filepath.Join(cacheDir, e.Name())
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var entry cacheEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		cached = append(cached, cachedItem{entry: entry, metaPath: metaPath})
	}
	slices.SortFunc(cached, func(a, b cachedItem) int {
		return cmp.Or(
			strings.Compare(a.entry.ImageRef, b.entry.ImageRef),
			strings.Compare(a.entry.Platform, b.entry.Platform))
	})
	return cached, nil
}

func clearCache() error {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return errors.New("could not determine cache directory")
	}
	return os.RemoveAll(cacheDir)
}

// removeCacheEntries removes every cached platform of an image and reports how
// many entries were deleted.
func removeCacheEntries(image string) (int, error) {
	normalized, err := normalizeRef(image)
	if err != nil {
		return 0, fmt.Errorf("parse image reference: %w", err)
	}
	items, err := listCache()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, it := range items {
		// Raw match keeps entries written before references were normalized removable
		if it.entry.ImageRef != normalized && it.entry.ImageRef != image {
			continue
		}
		tarPath := strings.TrimSuffix(it.metaPath, ".json") + ".tar"
		if err := os.Remove(tarPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		if err := os.Remove(it.metaPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func handleCacheCommand(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: pkgpulse cache <command>")
		fmt.Fprintln(os.Stderr, "\nCommands:")
		fmt.Fprintln(os.Stderr, "  list    List cached images")
		fmt.Fprintln(os.Stderr, "  clear   Remove all cached images")
		fmt.Fprintln(os.Stderr, "  rm      Remove specific cached image (all platforms)")
		fmt.Fprintln(os.Stderr, "  path    Show cache directory path")
		return 2
	}

	switch args[0] {
	case "list":
		items, err := listCache()
		if err != nil {
			fmt.Fprintf(os.Stderr, "pkgpulse: list cache: %v\n", err)
			return 1
		}
		if len(items) == 0 {
			fmt.Println("Cache is empty")
			return 0
		}
		fmt.Printf("%-46s %-14s %10s  %s\n", "IMAGE", "PLATFORM", "SIZE", "CACHED AT")
		fmt.Println(strings.Repeat("-", 90))
		var totalSize int64
		for _, it := range items {
			e := it.entry
			fmt.Printf("%-46s %-14s %7.1f MB  %s\n",
				trunc(displayRef(e.ImageRef), 46), cmp.Or(e.Platform, "-"),
				toMB(e.SizeBytes), e.CachedAt.Format("2006-01-02 15:04"))
			totalSize += e.SizeBytes
		}
		fmt.Println(strings.Repeat("-", 90))
		fmt.Printf("Total: %d images, %.1f MB\n", len(items), toMB(totalSize))

	case "clear":
		if err := clearCache(); err != nil {
			fmt.Fprintf(os.Stderr, "pkgpulse: clear cache: %v\n", err)
			return 1
		}
		fmt.Println("Cache cleared")

	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: pkgpulse cache rm <image>")
			return 2
		}
		n, err := removeCacheEntries(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "pkgpulse: remove cache entry: %v\n", err)
			return 1
		}
		if n == 0 {
			fmt.Fprintf(os.Stderr, "%s is not in the cache\n", args[1])
			return 1
		}
		fmt.Printf("Removed %s from cache (%d %s)\n", args[1], n, plural(n, "entry", "entries"))

	case "path":
		fmt.Println(getCacheDir())

	default:
		fmt.Fprintf(os.Stderr, "pkgpulse: unknown cache command: %s\n", args[0])
		return 2
	}
	return 0
}

/* ---- Entry point ---- */

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}

	for _, arg := range args {
		switch arg {
		case "--version", "-v":
			fmt.Printf("pkgpulse version %s\n", appVersion())
			return 0
		case "--help", "-h":
			printUsage(os.Stdout)
			return 0
		}
	}

	if args[0] == "cache" {
		return handleCacheCommand(args[1:])
	}

	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pkgpulse: %v\nRun 'pkgpulse --help' for usage.\n", err)
		return 2
	}
	return run(opts)
}

func run(opts options) int {
	results := analyzeAll(opts)

	var ok []imageResult
	for i, r := range results {
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] ✗ %s: %v\n", i+1, len(results), r.Image, r.Err)
			continue
		}
		fmt.Fprintf(os.Stderr, "[%d/%d] ✓ %s\n", i+1, len(results), r.Image)
		ok = append(ok, r)
	}
	for _, r := range results {
		for _, w := range r.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", r.Image, w)
		}
	}

	exitCode := 0
	if len(ok) < len(results) {
		exitCode = 1
	}

	if opts.jsonOut {
		if err := writeJSON(os.Stdout, results); err != nil {
			fmt.Fprintf(os.Stderr, "pkgpulse: write JSON: %v\n", err)
			return 1
		}
	} else if len(ok) > 0 {
		printTextResults(ok, opts.top)
	}

	if len(ok) == 0 {
		return exitCode
	}

	csvPath := opts.csvPath
	autoCSV := false
	if csvPath == "" && !opts.jsonOut && len(ok) > 3 {
		csvPath = nextFreePath(autoCSVName)
		autoCSV = true
	}
	if csvPath == "" {
		return exitCode
	}

	if len(ok) > 1 {
		if err := writeComparisonCSV(csvPath, ok); err != nil {
			fmt.Fprintf(os.Stderr, "pkgpulse: write comparison CSV: %v\n", err)
			return 1
		}
		if autoCSV {
			fmt.Fprintf(os.Stderr, "\nWrote CSV automatically: %s (summary + comparison table)\n", csvPath)
		} else {
			fmt.Fprintf(os.Stderr, "\nWrote CSV: %s (summary + comparison table)\n", csvPath)
		}
	} else {
		if err := writePackageCSV(csvPath, ok[0].Rows); err != nil {
			fmt.Fprintf(os.Stderr, "pkgpulse: write CSV: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "\nWrote CSV: %s (package,version,installed_MB)\n", csvPath)
	}
	return exitCode
}

// analyzeAll analyzes images in parallel with bounded concurrency. Failures
// are recorded per image so one bad reference doesn't abort the comparison.
func analyzeAll(opts options) []imageResult {
	n := len(opts.images)

	modeStr := ""
	if opts.noCache {
		modeStr += " (no cache)"
	}
	if opts.useSyft {
		modeStr += " (using syft)"
	}
	if opts.platform != defaultPlatform {
		modeStr += " (" + opts.platform + ")"
	}
	if n > 1 {
		fmt.Fprintf(os.Stderr, "Analyzing %d images in parallel%s...\n", n, modeStr)
	} else if modeStr != "" {
		fmt.Fprintf(os.Stderr, "Analyzing%s...\n", modeStr)
	}

	results := make([]imageResult, n)
	progressChan := make(chan progressEvent, 256)
	rendererDone := make(chan struct{})
	go runProgressRenderer(progressChan, opts.images, isTerminal(os.Stderr), rendererDone)

	sem := make(chan struct{}, opts.jobs)
	var wg sync.WaitGroup
	for i, image := range opts.images {
		wg.Go(func() {
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore
			send := func(ev progressEvent) { progressChan <- ev }
			res, err := analyzeImage(image, i, opts, send)
			if err != nil {
				res.Err = err
				send(progressEvent{idx: i, image: image, stage: "failed", message: err.Error()})
			}
			results[i] = res
		})
	}

	wg.Wait()
	close(progressChan)
	<-rendererDone
	return results
}

func analyzeImage(image string, idx int, opts options, sendProgress func(progressEvent)) (imageResult, error) {
	res := imageResult{Image: image, Platform: opts.platform}
	emit := func(stage, message string, current, totalSize int64, rateBps float64) {
		sendProgress(progressEvent{
			idx:       idx,
			image:     image,
			stage:     stage,
			message:   message,
			current:   current,
			totalSize: totalSize,
			rateBps:   rateBps,
		})
	}
	warn := func(format string, a ...any) {
		res.Warnings = append(res.Warnings, fmt.Sprintf(format, a...))
	}

	emit("resolving", "parsing image reference", 0, 0, 0)
	ref, err := name.ParseReference(image)
	if err != nil {
		return res, fmt.Errorf("parse image reference: %w", err)
	}
	platform, err := v1.ParsePlatform(opts.platform)
	if err != nil {
		return res, fmt.Errorf("parse platform: %w", err)
	}

	var downloaded, expected atomic.Int64
	remoteOpts := []remote.Option{
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(*platform),
		remote.WithTransport(&progressTransport{
			base:    http.DefaultTransport,
			onBytes: func(n int64) { downloaded.Add(n) },
		}),
	}

	// Cache images by default (syft pulls the image itself)
	useCache := !opts.noCache && !opts.useSyft
	key := cacheKey{ref: ref.Name(), platform: opts.platform}

	var img v1.Image
	if useCache {
		emit("cache_load", "checking local cache", 0, 0, 0)
		if cached, entry := loadFreshCache(ref, key, remoteOpts, emit, warn); cached != nil {
			img = cached
			res.Source = "cache"
			res.CompressedBytes = entry.CompressedBytes
			if res.CompressedBytes == 0 {
				res.CompressedBytes, _ = compressedSize(img)
			}
		}
	}

	stopDownload := func() {}
	if img == nil {
		res.Source = "registry"
		emit("manifest", "fetching from registry", 0, 0, 0)
		desc, err := remote.Get(ref, remoteOpts...)
		if err != nil {
			return res, fmt.Errorf("fetch manifest: %w", err)
		}
		remoteImg, err := desc.Image()
		if err != nil {
			return res, fmt.Errorf("resolve %s image: %w", opts.platform, err)
		}
		if res.CompressedBytes, err = compressedSize(remoteImg); err != nil {
			return res, fmt.Errorf("read manifest: %w", err)
		}
		img = remoteImg

		downloaded.Store(0)
		expected.Store(res.CompressedBytes)
		stopDownload = startDownloadProgress(&downloaded, &expected, emit)
		defer stopDownload()

		if useCache {
			emit("cache_save", "writing cache tarball", 0, 0, 0)
			entry := cacheEntry{
				ImageRef:        key.ref,
				Platform:        key.platform,
				RefDigest:       desc.Digest.String(),
				CompressedBytes: res.CompressedBytes,
			}
			if err := saveToCache(key, ref, remoteImg, entry); err != nil {
				warn("cache save failed: %v", err)
			} else {
				// Reload from cache for fast local layer reads
				stopDownload()
				emit("cache_reload", "reloading from cache", 0, 0, 0)
				if cached, _, err := loadFromCache(key); err == nil {
					img = cached
				} else {
					warn("cache reload failed: %v", err)
				}
			}
		}
	}

	if actual := imagePlatform(img); actual != nil {
		if !actual.Satisfies(v1.Platform{OS: platform.OS, Architecture: platform.Architecture}) {
			warn("image is %s, not the requested %s (registry has no %s variant)", actual, opts.platform, opts.platform)
		}
		res.Platform = actual.String()
	}

	var packages []pkg
	if opts.useSyft {
		stopDownload()
		emit("syft", "running syft scan", 0, 0, 0)
		if packages, err = runSyftAndParse(image, opts.platform); err != nil {
			return res, err
		}
	} else {
		emit("parsing", "extracting package databases", 0, 0, 0)
		var warnings []string
		packages, warnings, err = extractPackagesFromImage(img, func(message string, currentLayer, totalLayers int64) {
			emit("parsing", message, currentLayer, totalLayers, 0)
		})
		if err != nil {
			return res, fmt.Errorf("read layers: %w", err)
		}
		res.Warnings = append(res.Warnings, warnings...)
		stopDownload()
	}

	emit("processing", fmt.Sprintf("processing %d packages", len(packages)), int64(len(packages)), int64(len(packages)), 0)

	res.Rows = buildRows(packages)
	res.PackageMap = make(map[string]row, len(res.Rows))
	for _, r := range res.Rows {
		res.PackageMap[r.Name] = r
		res.InstalledBytes += r.Bytes
	}

	emit("done", "completed", 0, 0, 0)
	return res, nil
}

// loadFreshCache returns the cached image when present and still current. Tag
// references not verified within cacheRecheckAfter are checked against the
// registry with a cheap HEAD request, so mutable tags like :latest are
// refetched when they move. Digest references are immutable and never
// rechecked. If the registry is unreachable, the cached copy is used.
func loadFreshCache(ref name.Reference, key cacheKey, remoteOpts []remote.Option,
	emit func(stage, message string, current, totalSize int64, rateBps float64),
	warn func(format string, a ...any)) (v1.Image, *cacheEntry) {

	img, entry, err := loadFromCache(key)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warn("ignoring unreadable cache entry: %v", err)
		}
		return nil, nil
	}
	if _, isDigest := ref.(name.Digest); isDigest {
		return img, entry
	}
	lastVerified := entry.CachedAt
	if entry.CheckedAt.After(lastVerified) {
		lastVerified = entry.CheckedAt
	}
	if time.Since(lastVerified) < cacheRecheckAfter {
		return img, entry
	}

	emit("cache_check", "checking registry for updates", 0, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), freshnessTimeout)
	defer cancel()
	opts := append(slices.Clone(remoteOpts), remote.WithContext(ctx))

	current, err := remote.Head(ref, opts...)
	if err != nil {
		// Some registries don't support HEAD on manifests; fall back to GET
		var desc *remote.Descriptor
		if desc, err = remote.Get(ref, opts...); err == nil {
			current = &desc.Descriptor
		}
	}
	switch {
	case err != nil:
		warn("could not check registry for updates, using cached copy: %v", err)
	case current.Digest.String() != entry.RefDigest:
		emit("cache_check", "tag has moved, refetching", 0, 0, 0)
		return nil, nil
	default:
		entry.CheckedAt = time.Now()
		if _, metaPath := getCachePaths(key); metaPath != "" {
			_ = writeCacheMeta(metaPath, *entry) // best effort; only skips future checks
		}
	}
	return img, entry
}

func compressedSize(img v1.Image) (int64, error) {
	m, err := img.Manifest()
	if err != nil {
		return 0, err
	}
	total := m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total, nil
}

func imagePlatform(img v1.Image) *v1.Platform {
	cfg, err := img.ConfigFile()
	if err != nil || cfg.OS == "" {
		return nil
	}
	return &v1.Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}
}

// buildRows merges packages that share a name (multi-arch debs, multiple RPM
// versions) so totals, breakdowns and comparisons agree, then sorts by size
// descending with name as a tie-break for deterministic output.
func buildRows(packages []pkg) []row {
	byName := make(map[string]*row)
	for _, p := range packages {
		if p.SizeBytes <= 0 {
			continue
		}
		r, ok := byName[p.Name]
		if !ok {
			byName[p.Name] = &row{Name: p.Name, Ver: p.Version, Bytes: p.SizeBytes}
			continue
		}
		if !slices.Contains(strings.Split(r.Ver, ", "), p.Version) {
			r.Ver += ", " + p.Version
		}
		r.Bytes += p.SizeBytes
	}

	rows := make([]row, 0, len(byName))
	for _, r := range byName {
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), strings.Compare(a.Name, b.Name))
	})
	return rows
}

/* ---- Layer walking ---- */

// trackedFile is a file of interest in the merged image filesystem.
type trackedFile struct {
	data       []byte // contents (package databases only)
	size       int64
	isBinary   bool   // executable in one of binaryDirs
	inspected  bool   // binName/binVersion populated
	binName    string // package name reported for a binary
	binVersion string
}

func normalizeTarPath(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func isPackageDB(p string) bool {
	if p == dpkgDBPath || slices.Contains(apkDBPaths, p) {
		return true
	}
	if path.Dir(p) == dpkgStatusDir {
		return !strings.HasSuffix(p, ".md5sums")
	}
	return slices.ContainsFunc(rpmDBPaths, func(l rpmDBLocation) bool { return l.path == p })
}

// walkLayers replays the image layers as an overlay filesystem and returns the
// final state of every tracked file. Binaries are inspected inline until a
// package database has been seen, so binary-only images need a single pass.
func walkLayers(layers []v1.Layer, progress func(layer int)) (map[string]*trackedFile, error) {
	files := make(map[string]*trackedFile)
	sawPackageDB := false
	for i, layer := range layers {
		progress(i)
		added, err := readLayer(layer, files, &sawPackageDB)
		if err != nil {
			return nil, fmt.Errorf("layer %d: %w", i+1, err)
		}
		maps.Copy(files, added)
	}
	return files, nil
}

// readLayer applies the layer's deletions (whiteouts, opaque directories and
// replaced paths) to lower and returns the tracked files the layer adds.
// Deletions only affect lower layers, never entries from the same layer.
func readLayer(layer v1.Layer, lower map[string]*trackedFile, sawPackageDB *bool) (map[string]*trackedFile, error) {
	rc, err := layer.Uncompressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()

	// Ancestor directories of lower files, so subtree removal only scans when relevant
	lowerDirs := make(map[string]bool)
	for p := range lower {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			lowerDirs[d] = true
		}
	}
	removeUnder := func(dir string) {
		prefix := dir + "/"
		for k := range lower {
			if strings.HasPrefix(k, prefix) {
				delete(lower, k)
			}
		}
	}
	removeLower := func(p string) {
		delete(lower, p)
		if lowerDirs[p] {
			removeUnder(p)
		}
	}

	added := make(map[string]*trackedFile)
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		p := normalizeTarPath(hdr.Name)
		if p == "" {
			continue
		}
		dir, base := path.Dir(p), path.Base(p)

		if base == ".wh..wh..opq" {
			// Opaque directory: hide everything below it from lower layers
			if dir == "." {
				clear(lower)
			} else {
				removeUnder(dir)
			}
			continue
		}
		if target, ok := strings.CutPrefix(base, ".wh."); ok {
			removeLower(path.Join(dir, target))
			continue
		}
		if hdr.Typeflag == tar.TypeDir {
			// A directory replaces a lower file but merges with a lower directory
			delete(lower, p)
			continue
		}
		// Any other entry (file, symlink, hardlink) replaces whatever was below
		removeLower(p)

		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		switch {
		case isPackageDB(p):
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", p, err)
			}
			added[p] = &trackedFile{data: data, size: hdr.Size}
			*sawPackageDB = true
		case binaryDirs[dir] && hdr.Mode&0o111 != 0 && hdr.Size > 0:
			f := &trackedFile{size: hdr.Size, isBinary: true}
			if !*sawPackageDB {
				data, err := io.ReadAll(tr)
				if err != nil {
					return nil, fmt.Errorf("read %s: %w", p, err)
				}
				f.binName, f.binVersion = inspectBinary(base, data)
				f.inspected = true
			}
			added[p] = f
		}
	}
	return added, nil
}

// extractPackagesFromImage reads package databases from image layers.
func extractPackagesFromImage(img v1.Image, logProgress func(message string, currentLayer, totalLayers int64)) ([]pkg, []string, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, nil, err
	}

	n := int64(len(layers))
	logProgress(fmt.Sprintf("scanning %d layers", n), 0, n)
	files, err := walkLayers(layers, func(i int) {
		logProgress(fmt.Sprintf("layer %d/%d", i+1, n), int64(i+1), n)
	})
	if err != nil {
		return nil, nil, err
	}

	var packages []pkg
	var warnings []string

	for _, p := range apkDBPaths {
		if f, ok := files[p]; ok {
			logProgress("parsing apk database", n, n)
			packages = append(packages, parseAPKDB(f.data)...)
			break
		}
	}

	if f, ok := files[dpkgDBPath]; ok {
		logProgress("parsing dpkg database", n, n)
		packages = append(packages, parseDpkgDB(f.data, false)...)
	} else if parts := dpkgStatusParts(files); len(parts) > 0 {
		logProgress(fmt.Sprintf("combining %d dpkg status.d entries", len(parts)), n, n)
		packages = append(packages, parseDpkgDB(combineDpkgStatusParts(parts), true)...)
	}

	for _, loc := range rpmDBPaths {
		f, ok := files[loc.path]
		if !ok {
			continue
		}
		logProgress(fmt.Sprintf("parsing rpm database (%s)", loc.format), n, n)
		pkgs, err := parseRPMDB(f.data)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("rpm database %s (%s): %v", loc.path, loc.format, err))
		}
		packages = append(packages, pkgs...)
		break
	}

	// If no OS packages found, report executable binaries in the final filesystem.
	if len(packages) == 0 {
		logProgress("checking executable binaries", n, n)
		bins, err := binaryPackages(layers, files)
		if err != nil {
			return nil, warnings, err
		}
		packages = bins
	}

	return packages, warnings, nil
}

func dpkgStatusParts(files map[string]*trackedFile) map[string][]byte {
	parts := make(map[string][]byte)
	for p, f := range files {
		if path.Dir(p) == dpkgStatusDir {
			parts[p] = f.data
		}
	}
	return parts
}

// binaryPackages reports binaries in the final filesystem, one per name.
func binaryPackages(layers []v1.Layer, files map[string]*trackedFile) ([]pkg, error) {
	var paths []string
	pending := make(map[string]*trackedFile)
	for p, f := range files {
		if !f.isBinary {
			continue
		}
		paths = append(paths, p)
		if !f.inspected {
			pending[p] = f
		}
	}
	slices.Sort(paths)

	// Binaries seen after a package database (later deleted) still need a read
	if len(pending) > 0 {
		if err := inspectPendingBinaries(layers, pending); err != nil {
			return nil, err
		}
	}

	var packages []pkg
	seen := make(map[string]bool)
	for _, p := range paths {
		f := files[p]
		if !f.inspected || seen[f.binName] {
			continue
		}
		seen[f.binName] = true
		packages = append(packages, pkg{Name: f.binName, Version: f.binVersion, SizeBytes: f.size, Type: "binary"})
	}
	return packages, nil
}

// inspectPendingBinaries reads layers top-down so the first match for each
// path is its final version.
func inspectPendingBinaries(layers []v1.Layer, pending map[string]*trackedFile) error {
	for i := len(layers) - 1; i >= 0 && len(pending) > 0; i-- {
		rc, err := layers[i].Uncompressed()
		if err != nil {
			return fmt.Errorf("layer %d: %w", i+1, err)
		}
		tr := tar.NewReader(rc)
		for len(pending) > 0 {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = rc.Close()
				return fmt.Errorf("layer %d: %w", i+1, err)
			}
			p := normalizeTarPath(hdr.Name)
			f, ok := pending[p]
			if !ok || hdr.Typeflag != tar.TypeReg {
				continue
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				_ = rc.Close()
				return fmt.Errorf("layer %d: read %s: %w", i+1, p, err)
			}
			f.binName, f.binVersion = inspectBinary(path.Base(p), data)
			f.inspected = true
			delete(pending, p)
		}
		_ = rc.Close()
	}
	return nil
}

// inspectBinary derives a package name and version from an executable.
func inspectBinary(fileName string, data []byte) (pkgName, pkgVersion string) {
	pkgName, pkgVersion = fileName, "-"

	// Go build info is authoritative (a Go binary may merely mention BusyBox)
	if info, err := buildinfo.Read(bytes.NewReader(data)); err == nil {
		pkgVersion = info.GoVersion
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			pkgVersion = info.Main.Version
		}
		return pkgName, pkgVersion
	}

	// BusyBox applets may be named as individual commands ("[", "sh", etc.).
	// Normalize these to a single "busybox" package when signature is present.
	if match := busyBoxVersionRe.FindSubmatch(data); len(match) > 1 {
		return "busybox", string(match[1])
	}

	if pkgName == "getconf" {
		// libc-bin/getconf embeds glibc version strings in binaries.
		if match := debianGLIBCVersionRe.FindSubmatch(data); len(match) > 1 {
			pkgVersion = string(match[1])
		} else if match := glibcSymbolVersionRe.FindSubmatch(data); len(match) > 1 {
			pkgVersion = string(match[1])
		}
	}
	return pkgName, pkgVersion
}

/* ---- Package database parsers ---- */

// parseRPMDB parses RPM database using go-rpmdb (supports SQLite, BerkeleyDB, NDB)
func parseRPMDB(data []byte) ([]pkg, error) {
	// Write data to temp file (go-rpmdb needs file path)
	tmpFile, err := os.CreateTemp("", "rpmdb-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("write temp file: %w", err)
	}

	db, err := rpmdb.Open(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = db.Close() }()

	pkgList, err := db.ListPackages()
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}

	var packages []pkg
	for _, p := range pkgList {
		if p.Name != "" {
			packages = append(packages, pkg{
				Name:      p.Name,
				Version:   fmt.Sprintf("%s-%s", p.Version, p.Release),
				SizeBytes: int64(p.Size),
				Type:      "rpm",
			})
		}
	}
	return packages, nil
}

// lines iterates over data line by line without a maximum line length.
func lines(data []byte) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for line := range bytes.Lines(data) {
			if !yield(strings.TrimRight(string(line), "\r\n")) {
				return
			}
		}
	}
}

// parseAPKDB parses Alpine's lib/apk/db/installed format
func parseAPKDB(data []byte) []pkg {
	var packages []pkg
	var current pkg
	var installedSize, packageSize int64
	hasInstalledSize := false

	flush := func() {
		if current.Name != "" {
			current.Type = "apk"
			// I: is the installed size; S: (archive size) is only a fallback
			current.SizeBytes = installedSize
			if !hasInstalledSize {
				current.SizeBytes = packageSize
			}
			packages = append(packages, current)
		}
		current = pkg{}
		installedSize, packageSize, hasInstalledSize = 0, 0, false
	}

	for line := range lines(data) {
		if line == "" {
			flush()
			continue
		}
		if len(line) < 2 || line[1] != ':' {
			continue
		}

		value := line[2:]
		switch line[0] {
		case 'P': // Package name
			current.Name = value
		case 'V': // Version
			current.Version = value
		case 'I': // Installed size (bytes)
			if size, err := strconv.ParseInt(value, 10, 64); err == nil {
				installedSize, hasInstalledSize = size, true
			}
		case 'S': // Package size (bytes)
			if size, err := strconv.ParseInt(value, 10, 64); err == nil {
				packageSize = size
			}
		}
	}
	flush()

	return packages
}

func combineDpkgStatusParts(parts map[string][]byte) []byte {
	var combined bytes.Buffer
	for _, k := range slices.Sorted(maps.Keys(parts)) {
		data := parts[k]
		if len(data) == 0 {
			continue
		}
		combined.Write(data)
		if !bytes.HasSuffix(data, []byte("\n")) {
			combined.WriteByte('\n')
		}
		combined.WriteByte('\n')
	}
	return combined.Bytes()
}

// parseDpkgDB parses Debian's var/lib/dpkg/status format.
// If assumeInstalled is true, entries without a Status line are treated as installed.
func parseDpkgDB(data []byte, assumeInstalled bool) []pkg {
	var packages []pkg
	var current pkg
	var onDisk, statusSeen bool

	flush := func() {
		if current.Name != "" && (onDisk || (assumeInstalled && !statusSeen)) {
			current.Type = "deb"
			packages = append(packages, current)
		}
		current = pkg{}
		onDisk, statusSeen = false, false
	}

	for line := range lines(data) {
		if line == "" {
			flush()
			continue
		}

		// Skip continuation lines
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)

		switch key {
		case "Package":
			current.Name = value
		case "Version":
			current.Version = value
		case "Installed-Size":
			// dpkg stores size in KiB
			if size, err := strconv.ParseInt(value, 10, 64); err == nil {
				current.SizeBytes = size * 1024
			}
		case "Status":
			// "<want> <flag> <state>"; only count states with files on disk
			fields := strings.Fields(value)
			onDisk = len(fields) == 3 && dpkgStatesOnDisk[fields[2]]
			statusSeen = true
		}
	}
	flush()

	return packages
}

// runSyftAndParse runs syft and parses output (fallback mode)
func runSyftAndParse(image, platform string) ([]pkg, error) {
	cmd := exec.Command("syft", image,
		"--platform", platform,
		"--scope", "squashed",
		"--select-catalogers", defaultCatalogers,
		"-o", "syft-json")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("syft not found in PATH (install syft or drop --use-syft)")
		}
		return nil, fmt.Errorf("syft failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var sbom syftSBOM
	if err := json.Unmarshal(out.Bytes(), &sbom); err != nil {
		return nil, fmt.Errorf("parse syft-json: %w", err)
	}

	// Build file lookup map for binary packages
	fileMap := make(map[string]int64)
	for _, f := range sbom.Files {
		if f.Metadata.Size > 0 {
			fileMap[f.ID] = f.Metadata.Size
		}
	}

	artifactToFile := make(map[string]string)
	for _, rel := range sbom.ArtifactRelationships {
		if rel.Type == "evident-by" {
			artifactToFile[rel.Parent] = rel.Child
		}
	}

	var packages []pkg
	for _, a := range sbom.Artifacts {
		var sizeBytes int64
		switch a.Type {
		case "apk":
			sizeBytes = cmp.Or(a.Metadata.InstalledSize, a.Metadata.Size)
		case "rpm":
			sizeBytes = a.Metadata.Size
		case "deb":
			sizeBytes = a.Metadata.InstalledSize * 1024
		case "binary":
			if fileID, ok := artifactToFile[a.ID]; ok {
				sizeBytes = fileMap[fileID]
			}
		default:
			if a.Metadata.InstalledSize > 0 {
				sizeBytes = a.Metadata.InstalledSize * 1024
			} else {
				sizeBytes = a.Metadata.Size
			}
		}

		if sizeBytes > 0 {
			packages = append(packages, pkg{
				Name:      a.Name,
				Version:   a.Version,
				SizeBytes: sizeBytes,
				Type:      a.Type,
			})
		}
	}

	return packages, nil
}

/* ---- Output ---- */

func printTextResults(results []imageResult, top int) {
	fmt.Fprintln(os.Stderr)
	fmt.Println(strings.Repeat("=", 80))
	if len(results) > 1 {
		// Multiple images: only show comparison table (skip individual breakdowns)
		fmt.Println("COMPARISON")
		fmt.Println(strings.Repeat("=", 80) + "\n")
		displayComparisonTable(results, top)
	} else {
		// Single image: show detailed breakdown
		fmt.Println("RESULTS")
		fmt.Println(strings.Repeat("=", 80) + "\n")
		displayImageBreakdown(results[0], top)
	}
}

func formatCompressed(b int64) string {
	if b == 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.2f MB", toMB(b))
}

func displayImageBreakdown(result imageResult, top int) {
	fmt.Printf("Image: %s\n", result.Image)
	fmt.Printf("Platform: %s\n", result.Platform)
	fmt.Printf("Source: %s\n", result.Source)
	fmt.Printf("Compressed size (pull): %s\n", formatCompressed(result.CompressedBytes))
	fmt.Printf("Installed size (on disk): %.2f MB\n", toMB(result.InstalledBytes))
	fmt.Printf("Packages: %d\n\n", len(result.Rows))

	rows := result.Rows
	if top > 0 && top < len(rows) {
		rows = rows[:top]
	}
	fmt.Println("Packages by installed size (on-disk MB):")
	for _, r := range rows {
		fmt.Printf("  %-40s %-20s %8.2f MB\n", trunc(r.Name, 40), trunc(r.Ver, 20), r.MB())
	}
	if hidden := len(result.Rows) - len(rows); hidden > 0 {
		fmt.Printf("  ... %d more %s (showing top %d)\n", hidden, plural(hidden, "package", "packages"), top)
	}
	fmt.Println()
}

func displayComparisonTable(results []imageResult, top int) {
	// Summary comparison
	fmt.Println("Summary Comparison:")
	fmt.Printf("%-44s %-14s %8s %12s %12s %9s\n", "Image", "Platform", "Source", "Compressed", "Installed", "Packages")
	fmt.Println(strings.Repeat("-", 104))
	for _, r := range results {
		fmt.Printf("%-44s %-14s %8s %12s %12s %9d\n",
			trunc(r.Image, 44), trunc(r.Platform, 14), r.Source, formatCompressed(r.CompressedBytes),
			fmt.Sprintf("%.2f MB", toMB(r.InstalledBytes)), len(r.Rows))
	}
	fmt.Println()

	pkgNames := comparisonPackages(results, top)
	cells := buildComparisonMatrix(results, pkgNames)

	// Build header
	if top > 0 {
		fmt.Printf("Package Version & Size Comparison (top %d by size):\n", len(pkgNames))
	} else {
		fmt.Println("Package Version & Size Comparison:")
	}
	header := fmt.Sprintf("%-40s", "Package")
	for i := range results {
		header += fmt.Sprintf(" | %-18s %8s", fmt.Sprintf("Image %d Ver", i+1), "MB")
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", 40+len(results)*30))

	// Display packages
	for _, pkgName := range pkgNames {
		line := fmt.Sprintf("%-40s", trunc(pkgName, 40))
		for _, cell := range cells[pkgName] {
			if cell.Present {
				line += fmt.Sprintf(" | %-18s %8.2f", trunc(cell.Version, 18), cell.MB)
			} else {
				line += fmt.Sprintf(" | %-18s %8s", "-", "-")
			}
		}
		fmt.Println(line)
	}

	fmt.Println()
	for i, r := range results {
		fmt.Printf("Image %d: %s\n", i+1, r.Image)
	}
}

type comparisonCell struct {
	Version string
	MB      float64
	Present bool
}

// comparisonPackages returns every package name alphabetically, or when top > 0
// the top packages by their largest size in any image.
func comparisonPackages(results []imageResult, top int) []string {
	maxBytes := make(map[string]int64)
	for _, result := range results {
		for _, r := range result.Rows {
			maxBytes[r.Name] = max(maxBytes[r.Name], r.Bytes)
		}
	}

	names := slices.Sorted(maps.Keys(maxBytes))
	if top <= 0 || top >= len(names) {
		return names
	}
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(maxBytes[b], maxBytes[a]) })
	return names[:top]
}

func buildComparisonMatrix(results []imageResult, pkgNames []string) map[string][]comparisonCell {
	cells := make(map[string][]comparisonCell, len(pkgNames))
	for _, pkgName := range pkgNames {
		rowCells := make([]comparisonCell, len(results))
		for i, result := range results {
			if r, found := result.PackageMap[pkgName]; found {
				rowCells[i] = comparisonCell{
					Version: r.Ver,
					MB:      r.MB(),
					Present: true,
				}
			}
		}
		cells[pkgName] = rowCells
	}
	return cells
}

type jsonPackage struct {
	Name           string  `json:"name"`
	Version        string  `json:"version"`
	InstalledBytes int64   `json:"installed_bytes"`
	InstalledMB    float64 `json:"installed_mb"`
}

type jsonImage struct {
	Image           string        `json:"image"`
	Platform        string        `json:"platform,omitempty"`
	Source          string        `json:"source,omitempty"`
	CompressedBytes int64         `json:"compressed_bytes"`
	InstalledBytes  int64         `json:"installed_bytes"`
	PackageCount    int           `json:"package_count"`
	Packages        []jsonPackage `json:"packages"`
	Warnings        []string      `json:"warnings,omitempty"`
	Error           string        `json:"error,omitempty"`
}

type jsonReport struct {
	Version string      `json:"pkgpulse_version"`
	Images  []jsonImage `json:"images"`
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func writeJSON(w io.Writer, results []imageResult) error {
	report := jsonReport{Version: appVersion(), Images: make([]jsonImage, 0, len(results))}
	for _, r := range results {
		img := jsonImage{
			Image:           r.Image,
			Platform:        r.Platform,
			Source:          r.Source,
			CompressedBytes: r.CompressedBytes,
			InstalledBytes:  r.InstalledBytes,
			PackageCount:    len(r.Rows),
			Packages:        make([]jsonPackage, 0, len(r.Rows)),
			Warnings:        r.Warnings,
		}
		if r.Err != nil {
			img.Error = r.Err.Error()
		}
		for _, row := range r.Rows {
			img.Packages = append(img.Packages, jsonPackage{
				Name:           row.Name,
				Version:        row.Ver,
				InstalledBytes: row.Bytes,
				InstalledMB:    round2(row.MB()),
			})
		}
		report.Images = append(report.Images, img)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func sanitizeImageColumnName(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if isAlnum {
			if c >= 'A' && c <= 'Z' {
				c = c + ('a' - 'A')
			}
			b.WriteByte(c)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}

	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "image"
	}
	return name
}

func shortHash(s string, n int) string {
	h := sha256.Sum256([]byte(s))
	hexStr := hex.EncodeToString(h[:])
	if n <= 0 || n > len(hexStr) {
		return hexStr
	}
	return hexStr[:n]
}

func buildCSVImageColumns(results []imageResult) []string {
	columns := make([]string, len(results))
	seen := make(map[string]int)
	for i, r := range results {
		base := sanitizeImageColumnName(r.Image)
		seen[base]++
		if seen[base] == 1 {
			columns[i] = base
			continue
		}
		columns[i] = fmt.Sprintf("%s_%s", base, shortHash(r.Image, 8))
	}
	return columns
}

// nextFreePath returns p, or p with a numeric suffix if p already exists, so
// automatic exports never overwrite earlier results.
func nextFreePath(p string) string {
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	candidate := p
	for i := 2; ; i++ {
		if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
}

func writePackageCSV(path string, rows []row) (err error) {
	f, createErr := os.Create(path)
	if createErr != nil {
		return createErr
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	w := csv.NewWriter(f)
	if err := w.Write([]string{"package", "version", "installed_MB"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{r.Name, r.Ver, fmt.Sprintf("%.2f", r.MB())}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writeComparisonCSV(path string, results []imageResult) (err error) {
	f, createErr := os.Create(path)
	if createErr != nil {
		return createErr
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	w := csv.NewWriter(f)

	// Summary block
	if err := w.Write([]string{"section", "summary"}); err != nil {
		return err
	}
	if err := w.Write([]string{"image", "platform", "source", "compressed_MB", "installed_MB", "packages"}); err != nil {
		return err
	}
	for _, r := range results {
		compressed := "-"
		if r.CompressedBytes > 0 {
			compressed = fmt.Sprintf("%.2f", toMB(r.CompressedBytes))
		}
		if err := w.Write([]string{
			r.Image,
			r.Platform,
			r.Source,
			compressed,
			fmt.Sprintf("%.2f", toMB(r.InstalledBytes)),
			strconv.Itoa(len(r.Rows)),
		}); err != nil {
			return err
		}
	}

	// Separator + package comparison block
	if err := w.Write([]string{}); err != nil {
		return err
	}
	if err := w.Write([]string{"section", "packages"}); err != nil {
		return err
	}

	imageCols := buildCSVImageColumns(results)
	header := []string{"package"}
	for _, col := range imageCols {
		header = append(header, col+"_version", col+"_installed_MB")
	}
	if err := w.Write(header); err != nil {
		return err
	}

	pkgNames := comparisonPackages(results, 0)
	cells := buildComparisonMatrix(results, pkgNames)
	for _, pkgName := range pkgNames {
		out := []string{pkgName}
		for _, cell := range cells[pkgName] {
			if cell.Present {
				out = append(out, cell.Version, fmt.Sprintf("%.2f", cell.MB))
			} else {
				out = append(out, "-", "-")
			}
		}
		if err := w.Write(out); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func toMB(b int64) float64 { return float64(b) / (1024.0 * 1024.0) }

func printUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `pkgpulse - Container image size analyzer

Usage:
  pkgpulse [flags] <image-ref> [<image-ref>...]
  pkgpulse cache <command>

Flags:
  --help, -h          Show this help message
  --version, -v       Show version information
  --platform <p>      Platform to analyze for multi-arch images (default %s)
  --no-cache          Bypass cache, always fetch fresh from registry
  --use-syft          Use syft instead of native parsing (optional fallback)
  --csv <file>        Export package data to CSV file
  --json              Print results as JSON to stdout
  --top <n>           Show only the n largest packages (text output only)
  --jobs <n>          Number of images analyzed concurrently (default %d)

Cache Commands:
  pkgpulse cache list     List cached images with sizes
  pkgpulse cache clear    Remove all cached images
  pkgpulse cache rm IMG   Remove specific image from cache (all platforms)
  pkgpulse cache path     Show cache directory location

Image Resolution:
  1. Check local cache (tarballs stored in $XDG_CACHE_HOME/pkgpulse or ~/.cache/pkgpulse)
     - tags are re-checked against the registry and refetched if they moved
     - digest references (@sha256:...) are used from cache without a check
  2. Fetch from remote registry, save to cache

  Use --no-cache to skip cache and fetch fresh.

Examples:
  # Analyze any image (uses cache if available)
  pkgpulse alpine:latest
  pkgpulse postgres:latest mysql:latest

  # Analyze the arm64 variant of a multi-arch image
  pkgpulse --platform linux/arm64 alpine:latest

  # Force fresh fetch (bypass cache)
  pkgpulse --no-cache alpine:latest

  # Manage cache
  pkgpulse cache list
  pkgpulse cache clear

  # Export to CSV or JSON
  pkgpulse alpine:latest --csv packages.csv
  pkgpulse alpine:latest debian:12 --json > sizes.json

  # Auto-export CSV when comparing more than 3 images
  pkgpulse alpine:latest debian:12 ubuntu:24.04 busybox:latest

  # Use syft for edge cases (Rust binaries, unusual formats)
  pkgpulse --use-syft some-image:latest

Supported Registries:
  Works with any OCI-compliant registry (Docker Hub, GCR, ECR, GHCR, etc.)

Package Detection (all native, no external tools required):
  - APK (Alpine Linux, Wolfi)
  - DEB (Debian, Ubuntu, distroless)
  - RPM (RHEL, Fedora, CentOS, Oracle Linux, openSUSE)
  - Standalone binaries when no package database exists (Go build info, BusyBox)

Exit Status:
  0 all images analyzed, 1 one or more images failed, 2 usage error

`, defaultPlatform, defaultConcurrency)
}
