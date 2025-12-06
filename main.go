package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const version = "0.7.0"

// Default concurrency limit for parallel image analysis
const defaultConcurrency = 5

// Catalogers to use for package detection (skip language-specific ones for speed)
const defaultCatalogers = "apk,dpkg,rpm,binary"

/* ---- Minimal Syft JSON we need (syft-json schema) ---- */
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

/* ---- Layer info ---- */
type layerRec struct {
	Index  int
	Digest string
	SizeB  int64 // compressed blob size (pull size)
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
	var thoroughMode bool
	var localSource string // "docker" or "podman"
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--csv":
			if i+1 < len(os.Args) {
				csvOut = os.Args[i+1]
				i++ // skip next arg
			}
		case arg == "--thorough" || arg == "-t":
			thoroughMode = true
		case arg == "--local":
			localSource = "docker" // default to docker
		case arg == "--local=docker":
			localSource = "docker"
		case arg == "--local=podman":
			localSource = "podman"
		case strings.HasPrefix(arg, "--local="):
			localSource = strings.TrimPrefix(arg, "--local=")
		case arg == "--version" || arg == "-v" || arg == "--help" || arg == "-h":
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
	var modeStrs []string
	if thoroughMode {
		modeStrs = append(modeStrs, "thorough")
	}
	if localSource != "" {
		modeStrs = append(modeStrs, "local:"+localSource)
	}
	modeStr := ""
	if len(modeStrs) > 0 {
		modeStr = " (" + strings.Join(modeStrs, ", ") + ")"
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
			result := analyzeImage(img, idx, len(images), progressChan, thoroughMode, localSource)
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

func analyzeImage(image string, idx, total int, progressChan chan<- progressMsg, thoroughMode bool, localSource string) imageResult {
	prefix := fmt.Sprintf("[%d/%d]", idx+1, total)
	if total == 1 {
		prefix = ""
	}

	logProgress := func(msg string) {
		progressChan <- progressMsg{idx: idx, msg: msg}
	}

	// Run manifest fetch and syft scan in parallel
	logProgress(fmt.Sprintf("%s [%s] Fetching manifest...\n", prefix, image))

	var totalCompressed int64
	var sbom syftSBOM
	var manifestWg sync.WaitGroup

	// 1) Fetch manifest → layers (digest + compressed size) in parallel
	manifestWg.Go(func() {
		_, totalCompressed = fetchLayerSizes(image)
	})

	// 2) Get packages with sizes via Syft in parallel
	scanMode := "squashed"
	if thoroughMode {
		scanMode = "all-layers"
	}
	logProgress(fmt.Sprintf("%s [%s] Scanning packages with syft (%s)...\n", prefix, image, scanMode))

	manifestWg.Go(func() {
		sbom = runSyftJSON(image, thoroughMode, localSource)
	})

	manifestWg.Wait()

	// 3) Build package list with sizes
	logProgress(fmt.Sprintf("%s [%s] Processing %d packages...\n", prefix, image, len(sbom.Artifacts)))

	// Build file lookup map for binary packages
	fileMap := make(map[string]int64)
	for _, f := range sbom.Files {
		if f.Metadata.Size > 0 {
			fileMap[f.ID] = f.Metadata.Size
		}
	}

	// Build relationship map: artifact ID -> file ID
	artifactToFile := make(map[string]string)
	for _, rel := range sbom.ArtifactRelationships {
		if rel.Type == "evident-by" {
			artifactToFile[rel.Parent] = rel.Child
		}
	}

	rows := make([]row, 0, len(sbom.Artifacts))
	pkgMap := make(map[string]row)
	var totalInstalled int64
	for _, a := range sbom.Artifacts {
		var sizeKB int64
		// Determine size based on package type and available metadata
		switch a.Type {
		case "apk":
			// APK uses .installedSize in bytes (preferred) or .size
			if a.Metadata.InstalledSize > 0 {
				sizeKB = a.Metadata.InstalledSize / 1024
			} else if a.Metadata.Size > 0 {
				sizeKB = a.Metadata.Size / 1024
			}
		case "rpm":
			// RPM uses .size in bytes
			if a.Metadata.Size > 0 {
				sizeKB = a.Metadata.Size / 1024
			}
		case "deb":
			// DEB uses .installedSize in KB
			if a.Metadata.InstalledSize > 0 {
				sizeKB = a.Metadata.InstalledSize
			}
		case "binary":
			// Binary packages: look up file size via relationship
			if fileID, ok := artifactToFile[a.ID]; ok {
				if fileSize, ok := fileMap[fileID]; ok {
					sizeKB = fileSize / 1024
				}
			}
		default:
			// Fallback: try both fields (handles other package types)
			if a.Metadata.InstalledSize > 0 {
				sizeKB = a.Metadata.InstalledSize
			} else if a.Metadata.Size > 0 {
				sizeKB = a.Metadata.Size / 1024
			}
		}

		if sizeKB > 0 {
			totalInstalled += sizeKB
			r := row{
				Name: a.Name,
				Ver:  a.Version,
				MB:   float64(sizeKB) / 1024.0,
			}
			rows = append(rows, r)
			pkgMap[a.Name] = r
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
	}
}

func displayImageBreakdown(result imageResult) {
	fmt.Printf("Image: %s\n", result.Image)
	fmt.Printf("Compressed size (pull): %.2f MB\n", result.CompressedMB)
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
	fmt.Printf("%-55s %15s %15s %10s\n", "Image", "Compressed", "Installed", "Packages")
	fmt.Println(string(bytes.Repeat([]byte("-"), 97)))
	for _, r := range results {
		fmt.Printf("%-55s %15s %15s %10d\n",
			trunc(r.Image, 55), fmt.Sprintf("%.2f MB", r.CompressedMB),
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

func fetchLayerSizes(image string) ([]layerRec, int64) {
	ref, err := name.ParseReference(image)
	check(err)
	img, err := remote.Image(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	check(err)
	m, err := img.Manifest()
	check(err)
	layers := make([]layerRec, 0, len(m.Layers))
	var total int64
	for i, l := range m.Layers {
		layers = append(layers, layerRec{
			Index:  i,
			Digest: l.Digest.String(),
			SizeB:  l.Size,
		})
		total += l.Size
	}
	return layers, total
}

func runSyftJSON(image string, thoroughMode bool, localSource string) syftSBOM {
	scope := "squashed" // Default: faster, analyzes only final filesystem state
	if thoroughMode {
		scope = "all-layers" // Thorough: analyzes all layers, catches removed packages
	}

	// Build image source - use local daemon if specified
	imageSource := image
	if localSource != "" {
		imageSource = localSource + ":" + image
	}

	cmd := exec.Command("syft", imageSource,
		"--scope", scope,
		"--select-catalogers", defaultCatalogers,
		"-o", "syft-json")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("syft failed: %v\nstderr:\n%s", err, stderr.String())
	}
	var s syftSBOM
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		log.Fatalf("parse syft-json: %v", err)
	}
	return s
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
  --thorough, -t    Thorough mode: scan all layers (slower, catches removed packages)
  --local[=SOURCE]  Use local container daemon instead of registry pull
                    SOURCE can be: docker (default), podman
  --csv <file>      Export package data to CSV file

Examples:
  # Analyze a single image (fast by default)
  pkgpulse alpine:latest

  # Thorough analysis (scans all layers)
  pkgpulse --thorough alpine:latest

  # Use locally pulled image (faster for repeated analysis)
  pkgpulse --local postgres:latest
  pkgpulse --local=podman postgres:latest

  # Compare multiple images
  pkgpulse alpine:latest ubuntu:latest debian:latest

  # Export to CSV
  pkgpulse alpine:latest --csv packages.csv

Supported Registries:
  Works with any OCI-compliant registry (Docker Hub, GCR, ECR, GHCR, etc.)

Requirements:
  - syft (SBOM generation tool)
  - Go 1.21+ for building from source

`)
}
