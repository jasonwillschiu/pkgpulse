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
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

/* ---- Minimal Syft JSON we need (syft-json schema) ---- */
type syftSBOM struct {
	Artifacts []syftArtifact `json:"artifacts"`
}
type syftArtifact struct {
	Name     string         `json:"name"`
	Version  string         `json:"version"`
	Type     string         `json:"type"`
	Metadata syftMetadata   `json:"metadata"`
}
type syftMetadata struct {
	InstalledSize int64 `json:"installedSize"` // KB for deb
	Size          int64 `json:"size"`          // bytes for rpm and apk
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
	Image           string
	CompressedMB    float64
	InstalledMB     float64
	PackageCount    int
	Rows            []row
	PackageMap      map[string]row
}

type progressMsg struct {
	idx int
	msg string
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <image-ref> [<image-ref>...] [--csv packages.csv]\n", os.Args[0])
	}

	var images []string
	var csvOut string
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--csv" {
			if i+1 < len(os.Args) {
				csvOut = os.Args[i+1]
			}
			break
		}
		images = append(images, os.Args[i])
	}

	if len(images) == 0 {
		log.Fatalf("no images specified")
	}

	// Analyze images in parallel
	results := make([]imageResult, len(images))
	var wg sync.WaitGroup

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

	if len(images) > 1 {
		fmt.Fprintf(os.Stderr, "Analyzing %d images in parallel...\n\n", len(images))
	}

	for i, image := range images {
		wg.Add(1)
		go func(idx int, img string) {
			defer wg.Done()
			result := analyzeImage(img, idx, len(images), progressChan)
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

func analyzeImage(image string, idx, total int, progressChan chan<- progressMsg) imageResult {
	prefix := fmt.Sprintf("[%d/%d]", idx+1, total)
	if total == 1 {
		prefix = ""
	}

	logProgress := func(msg string) {
		progressChan <- progressMsg{idx: idx, msg: msg}
	}

	// 1) Fetch manifest → layers (digest + compressed size)
	logProgress(fmt.Sprintf("%s [%s] Fetching manifest...\n", prefix, image))
	_, totalCompressed := fetchLayerSizes(image)

	// 2) Get packages with sizes via Syft
	logProgress(fmt.Sprintf("%s [%s] Scanning packages with syft...\n", prefix, image))
	sbom := runSyftAllLayersJSON(image)

	// 3) Build package list with sizes
	logProgress(fmt.Sprintf("%s [%s] Processing %d packages...\n", prefix, image, len(sbom.Artifacts)))
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
		default:
			// Fallback: try both fields
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
		Image:           image,
		CompressedMB:    toMB(totalCompressed),
		InstalledMB:     float64(totalInstalled) / 1024.0,
		PackageCount:    len(rows),
		Rows:            rows,
		PackageMap:      pkgMap,
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

func runSyftAllLayersJSON(image string) syftSBOM {
	cmd := exec.Command("syft", image, "--scope", "all-layers", "-o", "syft-json")
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

func writeCSV(path string, rows []row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
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
