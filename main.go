package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

const version = "0.11.0"

// Default concurrency limit for parallel image analysis
const defaultConcurrency = 5

// Catalogers to use for syft fallback (skip language-specific ones for speed)
const defaultCatalogers = "apk,dpkg,rpm,binary"

// Package database paths
const (
	apkDBPath       = "lib/apk/db/installed"
	dpkgDBPath      = "var/lib/dpkg/status"
	dpkgStatusDir   = "var/lib/dpkg/status.d"
	rpmDBPathSqlite = "var/lib/rpm/rpmdb.sqlite"
	rpmDBPathBDB    = "var/lib/rpm/Packages"
	rpmDBPathNDB    = "var/lib/rpm/Packages.db"
)

var busyBoxVersionRe = regexp.MustCompile(`BusyBox v([0-9][0-9A-Za-z.+~:_-]*)`)
var debianGLIBCVersionRe = regexp.MustCompile(`\(Debian GLIBC ([^)]+)\)`)
var glibcSymbolVersionRe = regexp.MustCompile(`GLIBC_([0-9]+(?:\.[0-9]+){1,2})`)

// Cache metadata stored alongside tarball
type cacheEntry struct {
	ImageRef  string    `json:"image_ref"`
	Digest    string    `json:"digest"`
	CachedAt  time.Time `json:"cached_at"`
	SizeBytes int64     `json:"size_bytes"`
}

/* ---- Native package representation ---- */
type pkg struct {
	Name    string
	Version string
	SizeKB  int64
	Type    string // "apk", "deb", "rpm", "binary"
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
	MB        float64
}

type imageResult struct {
	Image        string
	CompressedMB float64
	InstalledMB  float64
	PackageCount int
	Rows         []row
	PackageMap   map[string]row
	Source       string // "local" or "remote"
}

type progressEvent struct {
	idx       int
	total     int
	image     string
	stage     string
	message   string
	current   int64
	totalSize int64
	rateBps   float64
	done      bool
}

type imageProgressState struct {
	image     string
	stage     string
	message   string
	current   int64
	totalSize int64
	rateBps   float64
	done      bool
	updatedAt time.Time
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
	default:
		return stage
	}
}

func formatBytesMB(n int64) float64 {
	return float64(n) / (1024.0 * 1024.0)
}

func buildProgressLine(idx, total, done int, s imageProgressState) string {
	line := fmt.Sprintf("[%d/%d] %s | %s", idx+1, total, trunc(s.image, 28), stageLabel(s.stage))

	if s.stage == "downloading" {
		currentMB := formatBytesMB(s.current)
		if s.totalSize > 0 {
			totalMB := formatBytesMB(s.totalSize)
			line += fmt.Sprintf(" %.1f/%.1f MB", currentMB, totalMB)
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
	if s.done {
		line += " | done"
	}
	line += fmt.Sprintf(" | overall %d/%d", done, total)
	return line
}

func runProgressRenderer(progressChan <-chan progressEvent, total int, done chan<- struct{}) {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	states := make([]imageProgressState, total)
	for i := range states {
		states[i].stage = "queued"
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

	closed := false
	for !closed {
		select {
		case ev, ok := <-progressChan:
			if !ok {
				closed = true
				break
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
				done:      ev.done,
				updatedAt: time.Now(),
			}
			render()
		case <-ticker.C:
			render()
		}
	}
	render()
	done <- struct{}{}
}

type comparisonCell struct {
	Version string
	MB      float64
	Present bool
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

func hashImageRef(ref string) string {
	h := sha256.Sum256([]byte(ref))
	return hex.EncodeToString(h[:8]) // First 8 bytes = 16 hex chars
}

func getCachePaths(imageRef string) (tarPath, metaPath string) {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return "", ""
	}
	hash := hashImageRef(imageRef)
	safeName := strings.ReplaceAll(imageRef, "/", "_")
	safeName = strings.ReplaceAll(safeName, ":", "_")
	baseName := fmt.Sprintf("%s_%s", safeName, hash)
	return filepath.Join(cacheDir, baseName+".tar"), filepath.Join(cacheDir, baseName+".json")
}

func loadFromCache(imageRef string, logProgress func(string)) (v1.Image, *cacheEntry, bool) {
	tarPath, metaPath := getCachePaths(imageRef)
	if tarPath == "" {
		return nil, nil, false
	}

	// Check if cache files exist
	if _, err := os.Stat(tarPath); os.IsNotExist(err) {
		return nil, nil, false
	}
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		return nil, nil, false
	}

	// Load metadata
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, nil, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(metaData, &entry); err != nil {
		return nil, nil, false
	}

	// Load image from tarball
	logProgress("Loading from cache...")
	img, err := tarball.ImageFromPath(tarPath, nil)
	if err != nil {
		logProgress(fmt.Sprintf("Cache read failed: %v", err))
		return nil, nil, false
	}

	return img, &entry, true
}

func saveToCache(imageRef string, img v1.Image, logProgress func(string)) error {
	tarPath, metaPath := getCachePaths(imageRef)
	if tarPath == "" {
		return fmt.Errorf("could not determine cache directory")
	}

	// Ensure cache directory exists
	cacheDir := getCacheDir()
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	logProgress("Saving to cache...")

	// Get image digest
	digest, err := img.Digest()
	if err != nil {
		return fmt.Errorf("get digest: %w", err)
	}

	// Write tarball
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse ref: %w", err)
	}
	if err := tarball.WriteToFile(tarPath, ref, img); err != nil {
		return fmt.Errorf("write tarball: %w", err)
	}

	// Get file size
	info, err := os.Stat(tarPath)
	if err != nil {
		return fmt.Errorf("stat tarball: %w", err)
	}

	// Write metadata
	entry := cacheEntry{
		ImageRef:  imageRef,
		Digest:    digest.String(),
		CachedAt:  time.Now(),
		SizeBytes: info.Size(),
	}
	metaData, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, metaData, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	return nil
}

func listCache() ([]cacheEntry, error) {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return nil, fmt.Errorf("could not determine cache directory")
	}

	entries, err := os.ReadDir(cacheDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cached []cacheEntry
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if err != nil {
			continue
		}
		var entry cacheEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		cached = append(cached, entry)
	}
	return cached, nil
}

func clearCache() error {
	cacheDir := getCacheDir()
	if cacheDir == "" {
		return fmt.Errorf("could not determine cache directory")
	}
	return os.RemoveAll(cacheDir)
}

func removeCacheEntry(imageRef string) error {
	tarPath, metaPath := getCachePaths(imageRef)
	if tarPath == "" {
		return fmt.Errorf("could not determine cache path")
	}
	_ = os.Remove(tarPath)
	_ = os.Remove(metaPath)
	return nil
}

func handleCacheCommand(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: pkgpulse cache <command>")
		fmt.Println("\nCommands:")
		fmt.Println("  list    List cached images")
		fmt.Println("  clear   Remove all cached images")
		fmt.Println("  rm      Remove specific cached image")
		fmt.Println("  path    Show cache directory path")
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		entries, err := listCache()
		if err != nil {
			log.Fatalf("list cache: %v", err)
		}
		if len(entries) == 0 {
			fmt.Println("Cache is empty")
			return
		}
		fmt.Printf("%-50s %10s %s\n", "IMAGE", "SIZE", "CACHED AT")
		fmt.Println(strings.Repeat("-", 80))
		var totalSize int64
		for _, e := range entries {
			sizeMB := float64(e.SizeBytes) / (1024 * 1024)
			fmt.Printf("%-50s %8.1f MB %s\n", trunc(e.ImageRef, 50), sizeMB, e.CachedAt.Format("2006-01-02 15:04"))
			totalSize += e.SizeBytes
		}
		fmt.Println(strings.Repeat("-", 80))
		fmt.Printf("Total: %d images, %.1f MB\n", len(entries), float64(totalSize)/(1024*1024))

	case "clear":
		if err := clearCache(); err != nil {
			log.Fatalf("clear cache: %v", err)
		}
		fmt.Println("Cache cleared")

	case "rm":
		if len(args) < 2 {
			log.Fatalf("usage: pkgpulse cache rm <image>")
		}
		if err := removeCacheEntry(args[1]); err != nil {
			log.Fatalf("remove cache entry: %v", err)
		}
		fmt.Printf("Removed %s from cache\n", args[1])

	case "path":
		fmt.Println(getCacheDir())

	default:
		log.Fatalf("unknown cache command: %s", args[0])
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Handle --version and --help flags
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Printf("pkgpulse version %s\n", version)
			os.Exit(0)
		}
		if arg == "--help" || arg == "-h" {
			printUsage()
			os.Exit(0)
		}
	}

	// Handle cache subcommands
	if os.Args[1] == "cache" {
		handleCacheCommand(os.Args[2:])
		return
	}

	var images []string
	var csvOut string
	var useSyft bool
	var noCache bool
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch arg {
		case "--csv":
			if i+1 < len(os.Args) {
				csvOut = os.Args[i+1]
				i++ // skip next arg
			}
		case "--use-syft":
			useSyft = true
		case "--no-cache":
			noCache = true
		case "--version", "-v", "--help", "-h":
			// Already handled above
		default:
			images = append(images, arg)
		}
	}

	if len(images) == 0 {
		log.Fatalf("no images specified")
	}

	// Analyze images in parallel with bounded concurrency
	results := make([]imageResult, len(images))
	var wg sync.WaitGroup

	// Semaphore to limit concurrent goroutines
	sem := make(chan struct{}, defaultConcurrency)

	// Channel for single-line progress renderer
	progressChan := make(chan progressEvent, 256)
	doneChan := make(chan struct{})
	go runProgressRenderer(progressChan, len(images), doneChan)

	// Build mode description for output
	modeStr := ""
	if noCache {
		modeStr = " (no cache)"
	}
	if useSyft {
		modeStr += " (using syft)"
	}

	if len(images) > 1 {
		fmt.Fprintf(os.Stderr, "Analyzing %d images in parallel%s...\n", len(images), modeStr)
	} else if modeStr != "" {
		fmt.Fprintf(os.Stderr, "Analyzing%s...\n", modeStr)
	}

	for i, image := range images {
		wg.Add(1)
		go func(idx int, img string) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore
			send := func(ev progressEvent) {
				progressChan <- ev
			}
			result := analyzeImage(img, idx, len(images), send, useSyft, noCache)
			results[idx] = result
		}(i, image)
	}

	wg.Wait()
	close(progressChan)
	<-doneChan

	// Print completion summary
	for i, img := range images {
		fmt.Fprintf(os.Stderr, "[%d/%d] ✓ %s\n", i+1, len(images), img)
	}

	// Display results
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Println(string(bytes.Repeat([]byte("="), 80)))

	if len(results) > 1 {
		// Multiple images: only show comparison table (skip individual breakdowns)
		fmt.Println("COMPARISON")
		fmt.Println(string(bytes.Repeat([]byte("="), 80)) + "\n")
		displayComparisonTable(results)
	} else {
		// Single image: show detailed breakdown
		fmt.Println("RESULTS")
		fmt.Println(string(bytes.Repeat([]byte("="), 80)) + "\n")
		displayImageBreakdown(results[0])
	}

	csvPath := csvOut
	autoCSV := false
	if csvPath == "" && len(results) > 3 {
		csvPath = "pkgpulse.csv"
		autoCSV = true
	}

	if csvPath != "" {
		if len(results) > 1 {
			if err := writeComparisonCSV(csvPath, results); err != nil {
				log.Fatalf("write comparison CSV: %v", err)
			}
			if autoCSV {
				fmt.Printf("\nWrote CSV automatically: %s (summary + comparison table)\n", csvPath)
			} else {
				fmt.Printf("\nWrote CSV: %s (summary + comparison table)\n", csvPath)
			}
		} else {
			if err := writePackageCSV(csvPath, results[0].Rows); err != nil {
				log.Fatalf("write CSV: %v", err)
			}
			fmt.Printf("\nWrote CSV: %s (package,version,installed_MB)\n", csvPath)
		}
	}
}

func analyzeImage(image string, idx, total int, sendProgress func(progressEvent), useSyft bool, noCache bool) imageResult {
	emit := func(stage, message string, current, totalSize int64, rateBps float64, done bool) {
		sendProgress(progressEvent{
			idx:       idx,
			total:     total,
			image:     image,
			stage:     stage,
			message:   message,
			current:   current,
			totalSize: totalSize,
			rateBps:   rateBps,
			done:      done,
		})
	}

	// Parse image reference
	emit("resolving", "parsing image reference", 0, 0, 0, false)
	ref, err := name.ParseReference(image)
	check(err)

	var img v1.Image
	var totalCompressed int64
	var sourceRemote bool
	source := "cache"

	var downloadedBytes atomic.Int64
	var estimatedTotalBytes atomic.Int64
	stopDownloadProgress := make(chan struct{})
	downloadProgressStopped := make(chan struct{})
	var stopDownloadOnce sync.Once

	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		defer close(downloadProgressStopped)
		lastBytes := int64(0)
		lastTick := time.Now()
		for {
			select {
			case <-stopDownloadProgress:
				return
			case <-ticker.C:
				current := downloadedBytes.Load()
				now := time.Now()
				delta := current - lastBytes
				rate := 0.0
				if dt := now.Sub(lastTick).Seconds(); dt > 0 {
					rate = float64(delta) / dt
				}
				lastBytes = current
				lastTick = now
				if current > 0 {
					emit("downloading", "pulling image bytes", current, estimatedTotalBytes.Load(), rate, false)
				}
			}
		}
	}()
	stopDownload := func() {
		stopDownloadOnce.Do(func() {
			close(stopDownloadProgress)
			<-downloadProgressStopped
		})
	}
	defer stopDownload()

	// Try cache first (unless --no-cache or --use-syft)
	if !noCache && !useSyft {
		emit("cache_load", "checking local cache", 0, 0, 0, false)
		if cachedImg, _, ok := loadFromCache(image, func(msg string) {
			emit("cache_load", msg, 0, 0, 0, false)
		}); ok {
			img = cachedImg
		}
	}

	// Fetch from registry if not in cache
	if img == nil {
		sourceRemote = true
		emit("manifest", "fetching from registry", 0, 0, 0, false)
		transport := &progressTransport{
			base: http.DefaultTransport,
			onBytes: func(n int64) {
				downloadedBytes.Add(n)
			},
		}
		opts := []remote.Option{
			remote.WithAuthFromKeychain(authn.DefaultKeychain),
			remote.WithTransport(transport),
		}
		remoteImg, remoteErr := remote.Image(ref, opts...)
		check(remoteErr)
		source = "remote"

		// Get compressed size from manifest
		manifest, err := remoteImg.Manifest()
		check(err)
		totalCompressed += manifest.Config.Size
		for _, l := range manifest.Layers {
			totalCompressed += l.Size
		}
		estimatedTotalBytes.Store(totalCompressed)
		emit("downloading", "pulling image bytes", downloadedBytes.Load(), totalCompressed, 0, false)

		// Save to cache and reload for consistent fast analysis
		if !noCache && !useSyft {
			emit("cache_save", "writing cache tarball", 0, 0, 0, false)
			if err := saveToCache(image, remoteImg, func(msg string) {
				emit("cache_save", msg, 0, 0, 0, false)
			}); err != nil {
				emit("cache_save", fmt.Sprintf("cache save failed: %v", err), 0, 0, 0, false)
				img = remoteImg // Fall back to remote image if cache fails
			} else {
				// Reload from cache for fast parallel analysis
				emit("cache_reload", "reloading from cache", 0, 0, 0, false)
				if cachedImg, _, ok := loadFromCache(image, func(msg string) {
					emit("cache_reload", msg, 0, 0, 0, false)
				}); ok {
					img = cachedImg
					source = "cached"
					sourceRemote = false
				} else {
					img = remoteImg
				}
			}
		} else {
			img = remoteImg
		}
	}

	var packages []pkg

	if useSyft {
		// Fallback to syft
		stopDownload()
		emit("syft", "running syft scan", 0, 0, 0, false)
		packages = runSyftAndParse(image)
	} else {
		// Native parsing
		emit("parsing", "extracting package databases", 0, 0, 0, false)
		packages = extractPackagesFromImage(img, func(message string, currentLayer, totalLayers int64) {
			emit("parsing", message, currentLayer, totalLayers, 0, false)
		})
		if sourceRemote {
			stopDownload()
		}
	}

	emit("processing", fmt.Sprintf("processing %d packages", len(packages)), int64(len(packages)), int64(len(packages)), 0, false)

	// Build output rows
	rows := make([]row, 0, len(packages))
	pkgMap := make(map[string]row)
	var totalInstalled int64

	for _, p := range packages {
		if p.SizeKB > 0 {
			totalInstalled += p.SizeKB
			r := row{
				Name: p.Name,
				Ver:  p.Version,
				MB:   float64(p.SizeKB) / 1024.0,
			}
			rows = append(rows, r)
			pkgMap[p.Name] = r
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].MB > rows[j].MB })
	emit("done", "completed", 0, 0, 0, true)

	return imageResult{
		Image:        image,
		CompressedMB: toMB(totalCompressed),
		InstalledMB:  float64(totalInstalled) / 1024.0,
		PackageCount: len(rows),
		Rows:         rows,
		PackageMap:   pkgMap,
		Source:       source,
	}
}

// extractPackagesFromImage reads package databases from image layers
func extractPackagesFromImage(img v1.Image, logProgress func(message string, currentLayer, totalLayers int64)) []pkg {
	layers, err := img.Layers()
	if err != nil {
		log.Printf("Warning: could not get layers: %v", err)
		return nil
	}

	totalLayers := len(layers)
	logProgress(fmt.Sprintf("scanning %d layers", totalLayers), 0, int64(totalLayers))

	// We want the final state, so read layers in order
	// and keep only the last version of each database file
	var apkData, dpkgData []byte
	dpkgStatusParts := make(map[string][]byte)
	dpkgFromStatusDir := false
	var rpmData []byte
	var rpmFormat string // "sqlite", "bdb", or "ndb"

	// Track potential Go binaries (executable files in common locations)
	goBinaries := make(map[string]int64) // path -> size

	for i, layer := range layers {
		logProgress(fmt.Sprintf("layer %d/%d", i+1, totalLayers), int64(i+1), int64(totalLayers))

		rc, err := layer.Uncompressed()
		if err != nil {
			continue
		}

		tr := tar.NewReader(rc)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			// Normalize path (remove leading /)
			path := strings.TrimPrefix(hdr.Name, "/")
			path = strings.TrimPrefix(path, "./")

			// Track dpkg status.d fragments (used by distroless)
			if strings.HasPrefix(path, dpkgStatusDir+"/") {
				base := filepath.Base(path)
				if target, found := strings.CutPrefix(base, ".wh."); found {
					if base == ".wh..wh..opq" {
						for k := range dpkgStatusParts {
							delete(dpkgStatusParts, k)
						}
					} else {
						delete(dpkgStatusParts, filepath.Join(dpkgStatusDir, target))
					}
					continue
				}
				if hdr.Typeflag == tar.TypeReg && !strings.HasSuffix(base, ".md5sums") {
					data, _ := io.ReadAll(tr)
					dpkgStatusParts[path] = data
				}
				continue
			}

			// Check for whiteout (deletion marker)
			if whiteoutBase, found := strings.CutPrefix(filepath.Base(path), ".wh."); found {
				removedPath := whiteoutBase
				if dir := filepath.Dir(path); dir != "." {
					removedPath = filepath.Join(dir, whiteoutBase)
				}

				// This is a whiteout - file was deleted
				switch removedPath {
				case apkDBPath:
					apkData = nil
				case dpkgDBPath:
					dpkgData = nil
				case rpmDBPathSqlite:
					rpmData = nil
				case rpmDBPathBDB:
					rpmData = nil
				case rpmDBPathNDB:
					rpmData = nil
				}
				// Also handle whiteout of binaries
				delete(goBinaries, removedPath)
				continue
			}

			// Read package database files
			switch path {
			case apkDBPath:
				data, _ := io.ReadAll(tr)
				apkData = data
			case dpkgDBPath:
				data, _ := io.ReadAll(tr)
				dpkgData = data
			case rpmDBPathSqlite:
				data, _ := io.ReadAll(tr)
				rpmData = data
				rpmFormat = "sqlite"
			case rpmDBPathBDB:
				data, _ := io.ReadAll(tr)
				rpmData = data
				rpmFormat = "bdb"
			case rpmDBPathNDB:
				data, _ := io.ReadAll(tr)
				rpmData = data
				rpmFormat = "ndb"
			default:
				// Check for potential Go binaries (executable files in bin directories)
				if hdr.Typeflag == tar.TypeReg && hdr.Mode&0111 != 0 && hdr.Size > 0 {
					dir := filepath.Dir(path)
					if dir == "usr/bin" || dir == "usr/local/bin" || dir == "bin" || dir == "usr/sbin" || dir == "sbin" {
						goBinaries[path] = hdr.Size
					}
				}
			}
		}
		_ = rc.Close()
	}

	// Parse the databases we found
	var packages []pkg

	if len(dpkgData) == 0 && len(dpkgStatusParts) > 0 {
		logProgress(fmt.Sprintf("combining %d dpkg status.d entries", len(dpkgStatusParts)), int64(totalLayers), int64(totalLayers))
		dpkgData = combineDpkgStatusParts(dpkgStatusParts)
		dpkgFromStatusDir = true
	}

	if len(apkData) > 0 {
		logProgress("parsing apk database", int64(totalLayers), int64(totalLayers))
		pkgs := parseAPKDB(apkData)
		packages = append(packages, pkgs...)
		logProgress(fmt.Sprintf("found %d apk packages", len(pkgs)), int64(totalLayers), int64(totalLayers))
	}
	if len(dpkgData) > 0 {
		logProgress("parsing dpkg database", int64(totalLayers), int64(totalLayers))
		pkgs := parseDpkgDB(dpkgData, dpkgFromStatusDir)
		packages = append(packages, pkgs...)
		logProgress(fmt.Sprintf("found %d deb packages", len(pkgs)), int64(totalLayers), int64(totalLayers))
	}
	if len(rpmData) > 0 {
		logProgress(fmt.Sprintf("parsing rpm database (%s)", rpmFormat), int64(totalLayers), int64(totalLayers))
		pkgs := parseRPMDB(rpmData, rpmFormat)
		packages = append(packages, pkgs...)
		logProgress(fmt.Sprintf("found %d rpm packages", len(pkgs)), int64(totalLayers), int64(totalLayers))
	}

	// If no OS packages found, inspect executable binaries in final filesystem state.
	if len(packages) == 0 && len(goBinaries) > 0 {
		logProgress(fmt.Sprintf("checking %d executable binaries", len(goBinaries)), int64(totalLayers), int64(totalLayers))
		packages = append(packages, detectBinaryPackages(img, goBinaries)...)
	}

	return packages
}

// parseRPMDB parses RPM database using go-rpmdb (supports SQLite, BerkeleyDB, NDB)
func parseRPMDB(data []byte, format string) []pkg {
	// Write data to temp file (go-rpmdb needs file path)
	tmpFile, err := os.CreateTemp("", "rpmdb-*")
	if err != nil {
		log.Printf("Warning: could not create temp file for RPM DB: %v", err)
		return nil
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		log.Printf("Warning: could not write RPM DB to temp file: %v", err)
		return nil
	}
	_ = tmpFile.Close()

	// Open RPM database
	db, err := rpmdb.Open(tmpFile.Name())
	if err != nil {
		log.Printf("Warning: could not open RPM DB (%s): %v", format, err)
		return nil
	}
	defer func() { _ = db.Close() }()

	// List packages
	pkgList, err := db.ListPackages()
	if err != nil {
		log.Printf("Warning: could not list RPM packages: %v", err)
		return nil
	}

	var packages []pkg
	for _, p := range pkgList {
		if p.Name != "" {
			packages = append(packages, pkg{
				Name:    p.Name,
				Version: fmt.Sprintf("%s-%s", p.Version, p.Release),
				SizeKB:  int64(p.Size) / 1024,
				Type:    "rpm",
			})
		}
	}

	return packages
}

// detectBinaryPackages inspects executable files and emits binary packages.
func detectBinaryPackages(img v1.Image, candidates map[string]int64) []pkg {
	var packages []pkg
	seenNames := make(map[string]struct{})

	layers, err := img.Layers()
	if err != nil {
		return nil
	}

	// Read layers in reverse so the first match is the final file version.
	for i := len(layers) - 1; i >= 0 && len(candidates) > 0; i-- {
		layer := layers[i]
		rc, err := layer.Uncompressed()
		if err != nil {
			continue
		}

		tr := tar.NewReader(rc)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			path := strings.TrimPrefix(hdr.Name, "/")
			path = strings.TrimPrefix(path, "./")

			size, isCandidate := candidates[path]
			if !isCandidate {
				continue
			}

			// Read binary data
			data, err := io.ReadAll(tr)
			if err != nil {
				continue
			}

			name := filepath.Base(path)
			version := "-"

			// BusyBox applets may be named as individual commands ("[", "sh", etc.).
			// Normalize these to a single "busybox" package when signature is present.
			if match := busyBoxVersionRe.FindSubmatch(data); len(match) > 1 {
				name = "busybox"
				version = string(match[1])
			}

			if _, exists := seenNames[name]; exists {
				delete(candidates, path)
				continue
			}
			seenNames[name] = struct{}{}

			// Try Go build info first.
			if info, err := buildinfo.Read(bytes.NewReader(data)); err == nil {
				version = info.GoVersion
				if info.Main.Version != "" && info.Main.Version != "(devel)" {
					version = info.Main.Version
				}
			} else if name == "getconf" {
				// libc-bin/getconf embeds glibc version strings in binaries.
				if match := debianGLIBCVersionRe.FindSubmatch(data); len(match) > 1 {
					version = string(match[1])
				} else {
					if match := glibcSymbolVersionRe.FindSubmatch(data); len(match) > 1 {
						version = string(match[1])
					}
				}
			}

			packages = append(packages, pkg{
				Name:    name,
				Version: version,
				SizeKB:  size / 1024,
				Type:    "binary",
			})

			// Remove from candidates so we don't process again
			delete(candidates, path)
		}
		_ = rc.Close()
	}

	return packages
}

// parseAPKDB parses Alpine's /lib/apk/db/installed format
func parseAPKDB(data []byte) []pkg {
	var packages []pkg
	var current pkg

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// End of package entry
			if current.Name != "" {
				packages = append(packages, current)
			}
			current = pkg{Type: "apk"}
			continue
		}

		if len(line) < 2 || line[1] != ':' {
			continue
		}

		key := line[0]
		value := line[2:]

		switch key {
		case 'P': // Package name
			current.Name = value
		case 'V': // Version
			current.Version = value
		case 'I': // Installed size (bytes)
			if size, err := strconv.ParseInt(value, 10, 64); err == nil {
				current.SizeKB = size / 1024
			}
		case 'S': // Package size (fallback if I not present)
			if current.SizeKB == 0 {
				if size, err := strconv.ParseInt(value, 10, 64); err == nil {
					current.SizeKB = size / 1024
				}
			}
		}
	}

	// Don't forget last package
	if current.Name != "" {
		packages = append(packages, current)
	}

	return packages
}

func combineDpkgStatusParts(parts map[string][]byte) []byte {
	if len(parts) == 0 {
		return nil
	}

	keys := make([]string, 0, len(parts))
	for k := range parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var combined bytes.Buffer
	for _, k := range keys {
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

// parseDpkgDB parses Debian's /var/lib/dpkg/status format.
// If assumeInstalled is true, entries without a Status line are treated as installed.
func parseDpkgDB(data []byte, assumeInstalled bool) []pkg {
	var packages []pkg
	var current pkg
	var isInstalled bool
	var statusSeen bool

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// End of package entry
			if current.Name != "" && (isInstalled || (assumeInstalled && !statusSeen)) {
				packages = append(packages, current)
			}
			current = pkg{Type: "deb"}
			isInstalled = false
			statusSeen = false
			continue
		}

		// Handle continuation lines (start with space)
		if strings.HasPrefix(line, " ") {
			continue
		}

		idx := strings.Index(line, ": ")
		if idx == -1 {
			continue
		}

		key := line[:idx]
		value := line[idx+2:]

		switch key {
		case "Package":
			current.Name = value
		case "Version":
			current.Version = value
		case "Installed-Size":
			// dpkg stores size in KB
			if size, err := strconv.ParseInt(value, 10, 64); err == nil {
				current.SizeKB = size
			}
		case "Status":
			// Only count installed packages
			isInstalled = strings.Contains(value, "installed")
			statusSeen = true
		}
	}

	// Don't forget last package
	if current.Name != "" && (isInstalled || (assumeInstalled && !statusSeen)) {
		packages = append(packages, current)
	}

	return packages
}

// runSyftAndParse runs syft and parses output (fallback mode)
func runSyftAndParse(image string) []pkg {
	cmd := exec.Command("syft", image,
		"--scope", "squashed",
		"--select-catalogers", defaultCatalogers,
		"-o", "syft-json")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("syft failed: %v\nstderr:\n%s", err, stderr.String())
	}

	var sbom syftSBOM
	if err := json.Unmarshal(out.Bytes(), &sbom); err != nil {
		log.Fatalf("parse syft-json: %v", err)
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
		var sizeKB int64
		switch a.Type {
		case "apk":
			if a.Metadata.InstalledSize > 0 {
				sizeKB = a.Metadata.InstalledSize / 1024
			} else if a.Metadata.Size > 0 {
				sizeKB = a.Metadata.Size / 1024
			}
		case "rpm":
			if a.Metadata.Size > 0 {
				sizeKB = a.Metadata.Size / 1024
			}
		case "deb":
			if a.Metadata.InstalledSize > 0 {
				sizeKB = a.Metadata.InstalledSize
			}
		case "binary":
			if fileID, ok := artifactToFile[a.ID]; ok {
				if fileSize, ok := fileMap[fileID]; ok {
					sizeKB = fileSize / 1024
				}
			}
		default:
			if a.Metadata.InstalledSize > 0 {
				sizeKB = a.Metadata.InstalledSize
			} else if a.Metadata.Size > 0 {
				sizeKB = a.Metadata.Size / 1024
			}
		}

		if sizeKB > 0 {
			packages = append(packages, pkg{
				Name:    a.Name,
				Version: a.Version,
				SizeKB:  sizeKB,
				Type:    a.Type,
			})
		}
	}

	return packages
}

func displayImageBreakdown(result imageResult) {
	fmt.Printf("Image: %s\n", result.Image)
	fmt.Printf("Source: %s\n", result.Source)
	if result.CompressedMB > 0 {
		fmt.Printf("Compressed size (pull): %.2f MB\n", result.CompressedMB)
	} else {
		fmt.Printf("Compressed size (pull): N/A (local image)\n")
	}
	fmt.Printf("Installed size (on disk): %.2f MB\n", result.InstalledMB)
	fmt.Printf("Packages: %d\n\n", result.PackageCount)

	fmt.Println("Packages by installed size (on-disk MB):")
	for _, r := range result.Rows {
		fmt.Printf("  %-40s %-20s %8.2f MB\n", trunc(r.Name, 40), trunc(r.Ver, 20), r.MB)
	}
	fmt.Println()
}

func displayComparisonTable(results []imageResult) {
	pkgNames, cells := buildComparisonMatrix(results)

	// Summary comparison
	fmt.Println("Summary Comparison:")
	fmt.Printf("%-50s %8s %15s %15s %10s\n", "Image", "Source", "Compressed", "Installed", "Packages")
	fmt.Println(string(bytes.Repeat([]byte("-"), 102)))
	for _, r := range results {
		compressedStr := fmt.Sprintf("%.2f MB", r.CompressedMB)
		if r.CompressedMB == 0 {
			compressedStr = "N/A"
		}
		fmt.Printf("%-50s %8s %15s %15s %10d\n",
			trunc(r.Image, 50), r.Source, compressedStr,
			fmt.Sprintf("%.2f MB", r.InstalledMB), r.PackageCount)
	}
	fmt.Println()

	// Build header
	fmt.Println("Package Version & Size Comparison:")
	header := fmt.Sprintf("%-40s", "Package")
	for i := range results {
		header += fmt.Sprintf(" | %-18s %8s", fmt.Sprintf("Image %d Ver", i+1), "MB")
	}
	fmt.Println(header)
	sepWidth := 40 + len(results)*30
	fmt.Println(string(bytes.Repeat([]byte("-"), sepWidth)))

	// Display packages
	for _, pkg := range pkgNames {
		line := fmt.Sprintf("%-40s", trunc(pkg, 40))
		for _, cell := range cells[pkg] {
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

func buildComparisonMatrix(results []imageResult) ([]string, map[string][]comparisonCell) {
	allPackages := make(map[string]bool)
	for _, result := range results {
		for pkgName := range result.PackageMap {
			allPackages[pkgName] = true
		}
	}

	pkgNames := make([]string, 0, len(allPackages))
	for pkgName := range allPackages {
		pkgNames = append(pkgNames, pkgName)
	}
	sort.Strings(pkgNames)

	cells := make(map[string][]comparisonCell, len(pkgNames))
	for _, pkgName := range pkgNames {
		rowCells := make([]comparisonCell, len(results))
		for i, result := range results {
			if r, found := result.PackageMap[pkgName]; found {
				rowCells[i] = comparisonCell{
					Version: r.Ver,
					MB:      r.MB,
					Present: true,
				}
			}
		}
		cells[pkgName] = rowCells
	}

	return pkgNames, cells
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
	defer w.Flush()
	if err := w.Write([]string{"package", "version", "installed_MB"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{r.Name, r.Ver, fmt.Sprintf("%.2f", r.MB)}); err != nil {
			return err
		}
	}
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
	defer w.Flush()

	// Summary block
	if err := w.Write([]string{"section", "summary"}); err != nil {
		return err
	}
	if err := w.Write([]string{"image", "source", "compressed_MB", "installed_MB", "packages"}); err != nil {
		return err
	}
	for _, r := range results {
		compressed := "-"
		if r.CompressedMB > 0 {
			compressed = fmt.Sprintf("%.2f", r.CompressedMB)
		}
		if err := w.Write([]string{
			r.Image,
			r.Source,
			compressed,
			fmt.Sprintf("%.2f", r.InstalledMB),
			strconv.Itoa(r.PackageCount),
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

	pkgNames, cells := buildComparisonMatrix(results)
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

	return w.Error()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func toMB(b int64) float64 { return float64(b) / (1024.0 * 1024.0) }

func check(err error) {
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			log.Fatalf("required binary not found: %v", err)
		}
		log.Fatal(err)
	}
}

func printUsage() {
	fmt.Printf(`pkgpulse - Container image size analyzer

Usage:
  pkgpulse [flags] <image-ref> [<image-ref>...]
  pkgpulse cache <command>

Flags:
  --help, -h        Show this help message
  --version, -v     Show version information
  --no-cache        Bypass cache, always fetch fresh from registry
  --use-syft        Use syft instead of native parsing (optional fallback)
  --csv <file>      Export package data to CSV file

Cache Commands:
  pkgpulse cache list     List cached images with sizes
  pkgpulse cache clear    Remove all cached images
  pkgpulse cache rm IMG   Remove specific image from cache
  pkgpulse cache path     Show cache directory location

Image Resolution:
  1. Check local cache (tarballs stored in ~/.cache/pkgpulse/)
  2. Fetch from remote registry, save to cache

  Cached images enable fast parallel analysis.
  Use --no-cache to skip cache and fetch fresh.

Examples:
  # Analyze any image (uses cache if available)
  pkgpulse alpine:latest
  pkgpulse postgres:latest mysql:latest

  # Force fresh fetch (bypass cache)
  pkgpulse --no-cache alpine:latest

  # Manage cache
  pkgpulse cache list
  pkgpulse cache clear

  # Export to CSV
  pkgpulse alpine:latest --csv packages.csv

  # Auto-export CSV when comparing more than 3 images
  pkgpulse alpine:latest debian:12 ubuntu:24.04 busybox:latest

  # Use syft for edge cases (Rust binaries, unusual formats)
  pkgpulse --use-syft some-image:latest

Supported Registries:
  Works with any OCI-compliant registry (Docker Hub, GCR, ECR, GHCR, etc.)

Package Detection (all native, no external tools required):
  - APK (Alpine Linux)
  - DEB (Debian, Ubuntu)
  - RPM (RHEL, Fedora, CentOS, Oracle Linux)
  - Go binaries (detected via build info)

Requirements:
  - No external tools required for native mode
  - Network access to container registry
  - syft (only if using --use-syft flag)
  - Go 1.25+ for building from source

`)
}
