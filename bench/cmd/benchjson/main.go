// benchjson runs Go benchmarks and outputs results as JSON.
//
// It runs tests and benchmarks as separate processes to avoid the
// sync.Once(output.SetRoot) reentrancy problem: tests are run once
// with -count=1, benchmarks are run separately with the user's -count.
//
// Usage:
//
//	go run ./bench/cmd/benchjson                     # all benchmarks
//	go run ./bench/cmd/benchjson -bench BenchmarkRead # filtered
//	go run ./bench/cmd/benchjson -count 3            # multiple iterations
//	go run ./bench/cmd/benchjson -o results.json     # write to file
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type BenchResult struct {
	Name       string             `json:"name"`
	Iterations int                `json:"iterations"`
	NsOp       float64            `json:"ns_op"`
	BytesOp    int                `json:"bytes_op,omitempty"`
	AllocsOp   int                `json:"allocs_op,omitempty"`
	Custom     map[string]float64 `json:"custom,omitempty"`
}

type TestResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

type Report struct {
	GitCommit  string        `json:"git_commit"`
	Timestamp  string        `json:"timestamp"`
	GoVersion  string        `json:"go_version"`
	OS         string        `json:"os"`
	Arch       string        `json:"arch"`
	Benchmarks []BenchResult `json:"benchmarks,omitempty"`
	Tests      []TestResult  `json:"tests,omitempty"`
	AllPassed  bool          `json:"all_passed"`
	RawOutput  string        `json:"raw_output,omitempty"`
}

var (
	benchLine    = regexp.MustCompile(`^(Benchmark\S+)\s+(\d+)\s+([\d.]+)\s+ns/op(.*)`)
	bytesOp      = regexp.MustCompile(`(\d+)\s+B/op`)
	allocsOp     = regexp.MustCompile(`(\d+)\s+allocs/op`)
	customMetric = regexp.MustCompile(`([\d.]+)\s+([a-zA-Z_][a-zA-Z0-9_]*)`)
	testPass     = regexp.MustCompile(`--- PASS: (\S+)`)
	testFail     = regexp.MustCompile(`--- FAIL: (\S+)`)
)

func main() {
	benchFilter := flag.String("bench", ".", "benchmark filter pattern")
	count := flag.Int("count", 1, "number of benchmark iterations")
	outFile := flag.String("o", "", "output file (default: stdout)")
	includeRaw := flag.Bool("raw", false, "include raw output in JSON")
	flag.Parse()

	repoRoot := findRepoRoot()

	gitCommit := "unknown"
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		gitCommit = strings.TrimSpace(string(out))
	}

	report := Report{
		GitCommit: gitCommit,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		AllPassed: true,
	}

	var rawParts []string

	// Step 1: Run tests once in their own process (-count=1).
	// Each go test invocation gets a fresh process, so the sync.Once
	// for output.SetRoot does not carry state across runs.
	testArgs := []string{
		"test", "./bench/",
		"-run", "^Test",
		"-bench", "^$", // no benchmarks
		"-count", "1",
		"-timeout", "120s",
		"-v",
	}
	testCmd := exec.Command("go", testArgs...)
	testCmd.Dir = repoRoot
	testCmd.Stderr = os.Stderr
	testOut, testErr := testCmd.Output()
	rawParts = append(rawParts, string(testOut))

	if testErr != nil {
		report.AllPassed = false
	}
	parseTestResults(string(testOut), &report)

	// Step 2: Run benchmarks in a separate process with the user's -count.
	// Tests are skipped (-run '^$') so only benchmarks execute.
	benchArgs := []string{
		"test", "./bench/",
		"-run", "^$", // no tests
		"-bench", *benchFilter,
		"-benchmem",
		"-count", strconv.Itoa(*count),
		"-timeout", "300s",
	}
	benchCmd := exec.Command("go", benchArgs...)
	benchCmd.Dir = repoRoot
	benchCmd.Stderr = os.Stderr
	benchOut, benchErr := benchCmd.Output()
	rawParts = append(rawParts, string(benchOut))

	if benchErr != nil {
		report.AllPassed = false
	}
	parseBenchResults(string(benchOut), &report)

	if *includeRaw {
		report.RawOutput = strings.Join(rawParts, "\n---\n")
	}

	data, _ := json.MarshalIndent(report, "", "  ")

	if *outFile != "" {
		if err := os.WriteFile(*outFile, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing %s: %v\n", *outFile, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *outFile)
	} else {
		fmt.Println(string(data))
	}

	if !report.AllPassed {
		os.Exit(1)
	}
}

func parseTestResults(output string, report *Report) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if m := testPass.FindStringSubmatch(line); m != nil {
			report.Tests = append(report.Tests, TestResult{Name: m[1], Passed: true})
		} else if m := testFail.FindStringSubmatch(line); m != nil {
			report.Tests = append(report.Tests, TestResult{Name: m[1], Passed: false})
			report.AllPassed = false
		}
	}
}

func parseBenchResults(output string, report *Report) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		m := benchLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		iters, _ := strconv.Atoi(m[2])
		nsop, _ := strconv.ParseFloat(m[3], 64)
		br := BenchResult{
			Name:       m[1],
			Iterations: iters,
			NsOp:       nsop,
		}

		rest := m[4]
		if bm := bytesOp.FindStringSubmatch(rest); bm != nil {
			br.BytesOp, _ = strconv.Atoi(bm[1])
		}
		if am := allocsOp.FindStringSubmatch(rest); am != nil {
			br.AllocsOp, _ = strconv.Atoi(am[1])
		}

		// Custom metrics: remove known metrics, parse remainder
		cleaned := bytesOp.ReplaceAllString(rest, "")
		cleaned = allocsOp.ReplaceAllString(cleaned, "")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned != "" {
			for _, cm := range customMetric.FindAllStringSubmatch(cleaned, -1) {
				name := cm[2]
				if name == "op" || name == "B" || name == "allocs" {
					continue
				}
				val, _ := strconv.ParseFloat(cm[1], 64)
				if br.Custom == nil {
					br.Custom = make(map[string]float64)
				}
				br.Custom[name] = val
			}
		}

		report.Benchmarks = append(report.Benchmarks, br)
	}
}

func findRepoRoot() string {
	if out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	wd, _ := os.Getwd()
	return wd
}
