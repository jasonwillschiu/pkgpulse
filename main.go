package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"debug/buildinfo"
	"encoding/csv"
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

	_ "github.com/glebarez/go-sqlite" // SQLite driver for RPM DB
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	rpmdb "github.com/knqyf263/go-rpmdb/pkg"
)

const version = "0.9.0"

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

	var images []string
	var csvOut string
	var useSyft bool
	var forceRemote bool
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
		case "--remote":
			forceRemote = true
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

	// Progress printer goroutine - prints messages in order
	go func() {
		nextIdx := 0
		buffer := make(map[int][]string)
		for msg := range progressChan {
			buffer[msg.idx] = append(buffer[msg.idx], msg.msg)
			// Print all buffered messages that are ready
			for {
				if msgs, ok := buffer[nextIdx]; ok {
					for _, m := range msgs {
						fmt.Fprint(os.Stderr, m)
					}
					delete(buffer, nextIdx)
					nextIdx++
				} else {
					break
				}
			}
		}
		doneChan <- true
	}()

	// Build mode description for output
	modeStr := ""
	if forceRemote {
		modeStr = " (remote only)"
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
			result := analyzeImage(img, idx, len(images), progressChan, useSyft, forceRemote)
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

	// Display individual breakdowns
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Println(string(bytes.Repeat([]byte("="), 80)))
	fmt.Println("RESULTS")
	fmt.Println(string(bytes.Repeat([]byte("="), 80)) + "\n")

	for i, result := range results {
		if i > 0 {
			fmt.Println("\n" + string(bytes.Repeat([]byte("="), 80)))
			fmt.Println()
		}
		displayImageBreakdown(result)
	}

	// Display comparison table if multiple images
	if len(results) > 1 {
		fmt.Println("\n" + string(bytes.Repeat([]byte("="), 80)))
		fmt.Println("COMPARISON TABLE")
		fmt.Println(string(bytes.Repeat([]byte("="), 80)) + "\n")
		displayComparisonTable(results)
	}

	if csvOut != "" {
		if err := writeCSV(csvOut, results[0].Rows); err != nil {
			log.Fatalf("write CSV: %v", err)
		}
		fmt.Printf("\nWrote CSV: %s (package,version,installed_MB)\n", csvOut)
	}
}

func analyzeImage(image string, idx, total int, progressChan chan<- progressMsg, useSyft bool, forceRemote bool) imageResult {
	prefix := fmt.Sprintf("[%d/%d]", idx+1, total)
	if total == 1 {
		prefix = ""
	}

	logProgress := func(msg string) {
		progressChan <- progressMsg{idx: idx, msg: msg}
	}

	// Parse image reference
	ref, err := name.ParseReference(image)
	check(err)

	var img v1.Image
	var totalCompressed int64
	source := "remote"

	// Try local daemon first (unless --remote flag is set)
	if !forceRemote {
		logProgress(fmt.Sprintf("%s [%s] Checking local daemon...\n", prefix, image))
		localImg, localErr := daemon.Image(ref)
		if localErr == nil {
			img = localImg
			source = "local"
			logProgress(fmt.Sprintf("%s [%s] Found locally\n", prefix, image))
			// For local images, compressed size is not meaningful (already extracted)
			// We'll show 0 or skip it in output
		}
	}

	// Fall back to remote if not found locally or --remote flag is set
	if img == nil {
		logProgress(fmt.Sprintf("%s [%s] Fetching from registry...\n", prefix, image))
		remoteImg, remoteErr := remote.Image(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		check(remoteErr)
		img = remoteImg
		source = "remote"

		// Get compressed size from manifest (only available for remote)
		manifest, err := img.Manifest()
		check(err)
		for _, l := range manifest.Layers {
			totalCompressed += l.Size
		}
	}

	var packages []pkg

	if useSyft {
		// Fallback to syft
		logProgress(fmt.Sprintf("%s [%s] Scanning with syft...\n", prefix, image))
		packages = runSyftAndParse(image)
	} else {
		// Native parsing
		logProgress(fmt.Sprintf("%s [%s] Scanning packages...\n", prefix, image))
		packages = extractPackagesFromImage(img)
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
func extractPackagesFromImage(img v1.Image) []pkg {
	layers, err := img.Layers()
	if err != nil {
		log.Printf("Warning: could not get layers: %v", err)
		return nil
	}

	// We want the final state, so read layers in order
	// and keep only the last version of each database file
	var apkData, dpkgData []byte
	var rpmData []byte
	var rpmFormat string // "sqlite", "bdb", or "ndb"

	// Track potential Go binaries (executable files in common locations)
	goBinaries := make(map[string]int64) // path -> size

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
		packages = append(packages, parseAPKDB(apkData)...)
	}
	if len(dpkgData) > 0 {
		packages = append(packages, parseDpkgDB(dpkgData)...)
	}
	if len(rpmData) > 0 {
		packages = append(packages, parseRPMDB(rpmData, rpmFormat)...)
	}

	// If no OS packages found, try to detect Go binaries
	if len(packages) == 0 && len(goBinaries) > 0 {
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

Flags:
  --help, -h        Show this help message
  --version, -v     Show version information
  --remote          Skip local daemon check, always fetch from registry
  --use-syft        Use syft instead of native parsing (optional fallback)
  --csv <file>      Export package data to CSV file

Image Resolution:
  By default, pkgpulse checks your local Docker/Podman daemon first.
  If the image exists locally, it uses that (faster, no network).
  If not found locally, it fetches from the remote registry.
  Use --remote to skip the local check and always pull from registry.

Examples:
  # Analyze any image (checks local first, then remote)
  pkgpulse alpine:latest
  pkgpulse postgres:latest
  pkgpulse mysql:latest
  pkgpulse redhat/ubi9-micro

  # Force fetching from remote registry
  pkgpulse --remote alpine:latest

  # Compare multiple images
  pkgpulse alpine:latest postgres:latest mysql:latest

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
  - Docker or Podman daemon (optional, for local image check)
  - syft (only if using --use-syft flag)
  - Go 1.25+ for building from source

`)
}
