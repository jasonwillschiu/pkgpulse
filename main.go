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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/glebarez/go-sqlite" // SQLite driver for RPM DB
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	rpmdb "github.com/knqyf263/go-rpmdb/pkg"
)

const version = "0.10.1"

// Default concurrency limit for parallel image analysis
const defaultConcurrency = 5

// Catalogers to use for syft fallback (skip language-specific ones for speed)
const defaultCatalogers = "apk,dpkg,rpm,binary"

// Package database paths
const (
	apkDBPath       = "lib/apk/db/installed"
	dpkgDBPath      = "var/lib/dpkg/status"
	rpmDBPathSqlite = "var/lib/rpm/rpmdb.sqlite"
	rpmDBPathBDB    = "var/lib/rpm/Packages"
	rpmDBPathNDB    = "var/lib/rpm/Packages.db"
)

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
	Type    string // "apk", "deb", "rpm"
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

type progressMsg struct {
	idx int
	msg string
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

	// Channel for ordered progress output
	progressChan := make(chan progressMsg, 100)
	doneChan := make(chan bool)

	// Progress printer goroutine - prints messages immediately
	go func() {
		for msg := range progressChan {
			fmt.Fprint(os.Stderr, msg.msg)
		}
		doneChan <- true
	}()

	// Build mode description for output
	modeStr := ""
	if noCache {
		modeStr = " (no cache)"
	}
	if useSyft {
		modeStr += " (using syft)"
	}

	if len(images) > 1 {
		fmt.Fprintf(os.Stderr, "Analyzing %d images in parallel%s...\n\n", len(images), modeStr)
	} else if modeStr != "" {
		fmt.Fprintf(os.Stderr, "Analyzing%s...\n\n", modeStr)
	}

	for i, image := range images {
		wg.Add(1)
		go func(idx int, img string) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore
			logFunc := func(msg progressMsg) {
				progressChan <- msg
			}
			result := analyzeImage(img, idx, len(images), logFunc, useSyft, noCache)
			results[idx] = result
		}(i, image)
	}

	wg.Wait()
	close(progressChan)
	<-doneChan

	// Print completion summary
	fmt.Fprintf(os.Stderr, "\n")
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

	if csvOut != "" {
		if err := writeCSV(csvOut, results[0].Rows); err != nil {
			log.Fatalf("write CSV: %v", err)
		}
		fmt.Printf("\nWrote CSV: %s (package,version,installed_MB)\n", csvOut)
	}
}

func analyzeImage(image string, idx, total int, sendProgress func(progressMsg), useSyft bool, noCache bool) imageResult {
	prefix := fmt.Sprintf("[%d/%d]", idx+1, total)
	if total == 1 {
		prefix = ""
	}

	logProgress := func(msg string) {
		sendProgress(progressMsg{idx: idx, msg: msg})
	}

	// Parse image reference
	ref, err := name.ParseReference(image)
	check(err)

	var img v1.Image
	var totalCompressed int64
	source := "cache"

	// Try cache first (unless --no-cache or --use-syft)
	if !noCache && !useSyft {
		if cachedImg, _, ok := loadFromCache(image, func(msg string) {
			logProgress(fmt.Sprintf("%s [%s] %s\n", prefix, image, msg))
		}); ok {
			img = cachedImg
		}
	}

	// Fetch from registry if not in cache
	if img == nil {
		logProgress(fmt.Sprintf("%s [%s] Fetching from registry...\n", prefix, image))
		remoteImg, remoteErr := remote.Image(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		check(remoteErr)
		source = "remote"

		// Get compressed size from manifest
		manifest, err := remoteImg.Manifest()
		check(err)
		for _, l := range manifest.Layers {
			totalCompressed += l.Size
		}

		// Save to cache and reload for consistent fast analysis
		if !noCache && !useSyft {
			if err := saveToCache(image, remoteImg, func(msg string) {
				logProgress(fmt.Sprintf("%s [%s] %s\n", prefix, image, msg))
			}); err != nil {
				logProgress(fmt.Sprintf("%s [%s] Cache save failed: %v\n", prefix, image, err))
				img = remoteImg // Fall back to remote image if cache fails
			} else {
				// Reload from cache for fast parallel analysis
				if cachedImg, _, ok := loadFromCache(image, func(msg string) {
					logProgress(fmt.Sprintf("%s [%s] %s\n", prefix, image, msg))
				}); ok {
					img = cachedImg
					source = "cached"
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
		logProgress(fmt.Sprintf("%s [%s] Scanning with syft...\n", prefix, image))
		packages = runSyftAndParse(image)
	} else {
		// Native parsing
		packages = extractPackagesFromImage(img, func(msg string) {
			logProgress(fmt.Sprintf("%s [%s] %s\n", prefix, image, msg))
		})
	}

	logProgress(fmt.Sprintf("%s [%s] Processing %d packages...\n", prefix, image, len(packages)))

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
func extractPackagesFromImage(img v1.Image, logProgress func(string)) []pkg {
	layers, err := img.Layers()
	if err != nil {
		log.Printf("Warning: could not get layers: %v", err)
		return nil
	}

	totalLayers := len(layers)
	logProgress(fmt.Sprintf("Scanning %d layers...", totalLayers))

	// We want the final state, so read layers in order
	// and keep only the last version of each database file
	var apkData, dpkgData []byte
	var rpmData []byte
	var rpmFormat string // "sqlite", "bdb", or "ndb"

	// Track potential Go binaries (executable files in common locations)
	goBinaries := make(map[string]int64) // path -> size

	for i, layer := range layers {
		logProgress(fmt.Sprintf("Layer %d/%d...", i+1, totalLayers))

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

			// Check for whiteout (deletion marker)
			if whiteoutBase, found := strings.CutPrefix(path, ".wh."); found {
				// This is a whiteout - file was deleted
				switch whiteoutBase {
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
				delete(goBinaries, whiteoutBase)
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

	if len(apkData) > 0 {
		logProgress("Found APK database, parsing...")
		pkgs := parseAPKDB(apkData)
		packages = append(packages, pkgs...)
		logProgress(fmt.Sprintf("Found %d APK packages", len(pkgs)))
	}
	if len(dpkgData) > 0 {
		logProgress("Found dpkg database, parsing...")
		pkgs := parseDpkgDB(dpkgData)
		packages = append(packages, pkgs...)
		logProgress(fmt.Sprintf("Found %d deb packages", len(pkgs)))
	}
	if len(rpmData) > 0 {
		logProgress(fmt.Sprintf("Found RPM database (%s), parsing...", rpmFormat))
		pkgs := parseRPMDB(rpmData, rpmFormat)
		packages = append(packages, pkgs...)
		logProgress(fmt.Sprintf("Found %d RPM packages", len(pkgs)))
	}

	// If no OS packages found, try to detect Go binaries
	if len(packages) == 0 && len(goBinaries) > 0 {
		logProgress(fmt.Sprintf("No OS packages, checking %d binaries for Go...", len(goBinaries)))
		packages = append(packages, detectGoBinaries(img, goBinaries)...)
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

// detectGoBinaries checks executable files for Go build info
func detectGoBinaries(img v1.Image, candidates map[string]int64) []pkg {
	var packages []pkg

	layers, err := img.Layers()
	if err != nil {
		return nil
	}

	// Read each candidate binary and check if it's a Go binary
	for _, layer := range layers {
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

			// Try to read Go build info
			info, err := buildinfo.Read(bytes.NewReader(data))
			if err != nil {
				continue // Not a Go binary
			}

			// Extract binary name and version
			name := filepath.Base(path)
			version := info.GoVersion
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = info.Main.Version
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

// parseDpkgDB parses Debian's /var/lib/dpkg/status format
func parseDpkgDB(data []byte) []pkg {
	var packages []pkg
	var current pkg
	var isInstalled bool

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// End of package entry
			if current.Name != "" && isInstalled {
				packages = append(packages, current)
			}
			current = pkg{Type: "deb"}
			isInstalled = false
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
		}
	}

	// Don't forget last package
	if current.Name != "" && isInstalled {
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

	// Collect all unique package names
	allPackages := make(map[string]bool)
	for _, result := range results {
		for pkg := range result.PackageMap {
			allPackages[pkg] = true
		}
	}

	// Convert to sorted slice
	pkgNames := make([]string, 0, len(allPackages))
	for pkg := range allPackages {
		pkgNames = append(pkgNames, pkg)
	}
	sort.Strings(pkgNames)

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
		for _, result := range results {
			if r, found := result.PackageMap[pkg]; found {
				line += fmt.Sprintf(" | %-18s %8.2f", trunc(r.Ver, 18), r.MB)
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

func writeCSV(path string, rows []row) (err error) {
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
