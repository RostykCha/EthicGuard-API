// cover-check reports per-package coverage from a Go coverage profile and
// optionally fails the build if any covered package is below a floor.
//
// Usage:
//
//	cover-check -profile coverage.out -floor 70           # report only
//	cover-check -profile coverage.out -floor 70 -enforce  # fail below floor
//
// Excluded paths (never count toward the floor):
//   - cmd/* (entry points; covered by integration runs, not unit tests)
//   - scripts/* (build-time tooling like this checker itself)
//   - internal/version (trivial constants)
//   - internal/store/migrations (SQL only, no Go statements)
//
// Packages with zero statements (no executable code) are reported but never
// fail the floor — they're neither covered nor uncovered.
//
// AGENT-NOTE: this is the CI gate. If you exclude another package, document
// the reason in CLAUDE.md "Code conventions" so future readers know it's
// intentional, not a forgotten oversight.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
)

func main() {
	profile := flag.String("profile", "coverage.out", "path to a Go coverage profile")
	floor := flag.Float64("floor", 70.0, "minimum percentage for a covered package")
	enforce := flag.Bool("enforce", false, "exit non-zero when any covered package is below the floor")
	flag.Parse()

	excludes := []string{
		"github.com/ethicguard/ethicguard-api/cmd",
		"github.com/ethicguard/ethicguard-api/scripts",
		"github.com/ethicguard/ethicguard-api/internal/version",
		"github.com/ethicguard/ethicguard-api/internal/store/migrations",
	}

	stats, err := parseProfile(*profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cover-check: %v\n", err)
		os.Exit(2)
	}

	pkgs := make([]string, 0, len(stats))
	for p := range stats {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)

	failed := false
	for _, pkg := range pkgs {
		st := stats[pkg]
		if isExcluded(pkg, excludes) {
			fmt.Printf("EXCLUDED  %s\n", pkg)
			continue
		}
		if st.total == 0 {
			fmt.Printf("NOSTMTS   %s\n", pkg)
			continue
		}
		pct := 100.0 * float64(st.covered) / float64(st.total)
		marker := "OK     "
		if pct < *floor {
			marker = "BELOW  "
			failed = true
		}
		fmt.Printf("%s %5.1f%%  %s\n", marker, pct, pkg)
	}

	if failed && *enforce {
		fmt.Fprintf(os.Stderr, "\nERROR: at least one covered package is below the %.0f%% floor\n", *floor)
		os.Exit(1)
	}
	if failed {
		fmt.Fprintln(os.Stderr, "\n(floor not enforced — pass -enforce to fail builds on regressions)")
	}
}

type pkgStat struct {
	covered int
	total   int
}

// parseProfile reads a Go coverage profile and aggregates statements by the
// directory of each file (which is the import path of the package).
//
// Profile format reminder:
//
//	mode: set
//	<importpath>/<file>.go:start.col,end.col <numStmts> <count>
//	...
func parseProfile(filename string) (map[string]pkgStat, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filename, err)
	}
	defer f.Close()

	stats := map[string]pkgStat{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return nil, fmt.Errorf("expected `mode:` header, got %q", line)
			}
			continue
		}
		// line: <filepath>:<loc> <nstmts> <count>
		// Walk from the right because filepaths can contain spaces in theory
		// (they don't here, but keep the parser robust).
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		fileLoc := strings.Join(fields[:len(fields)-2], " ")
		nstmts := parseInt(fields[len(fields)-2])
		count := parseInt(fields[len(fields)-1])

		colon := strings.Index(fileLoc, ":")
		if colon < 0 {
			continue
		}
		file := fileLoc[:colon]
		pkg := path.Dir(file)

		st := stats[pkg]
		st.total += nstmts
		if count > 0 {
			st.covered += nstmts
		}
		stats[pkg] = st
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", filename, err)
	}
	return stats, nil
}

func parseInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func isExcluded(pkg string, excludes []string) bool {
	for _, ex := range excludes {
		if pkg == ex || strings.HasPrefix(pkg, ex+"/") {
			return true
		}
	}
	return false
}
