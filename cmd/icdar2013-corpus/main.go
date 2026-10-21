// Command icdar2013-corpus runs the complete pinned document corpus against two
// library checkouts using one public-API extraction driver and shared scoring.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/coregx/gxpdf/internal/benchmark/icdar2013"
)

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, fmt.Errorf("worker output exceeds byte limit")
	}
	return b.Buffer.Write(data)
}

func command(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	var output cappedBuffer
	output.limit = 32 << 20
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", args[0], err, output.String())
	}
	return output.Bytes(), nil
}

func hash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func fileHash(name string) (string, error) {
	return boundedFileHash(name, 256<<20)
}

func boundedFileHash(name string, limit int64) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	count, err := io.Copy(digest, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if count > limit {
		return "", fmt.Errorf("provenance file exceeds limit: %s", name)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

// sourceHash includes actual tracked source plus new benchmark tooling, not
// merely HEAD. It remains accurate when the implementation is uncommitted.
func sourceHash(ctx context.Context, dir string) (string, error) {
	data, err := command(ctx, dir, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", err
	}
	names := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	sort.Strings(names)
	var input strings.Builder
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") && name != "go.mod" && name != "go.sum" && name != "internal/benchmark/icdar2013/document-index.json" {
			continue
		}
		digest, readErr := fileHash(filepath.Join(dir, name))
		if readErr != nil {
			return "", readErr
		}
		fmt.Fprintf(&input, "%s\x00%s\n", name, digest)
	}
	return hash([]byte(input.String())), nil
}

func build(ctx context.Context, dir, driver, binary string) (icdar2013.DocumentBuild, error) {
	var result icdar2013.DocumentBuild
	revision, err := command(ctx, dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.Revision = strings.TrimSpace(string(revision))
	result.SourceSHA256, err = sourceHash(ctx, dir)
	if err != nil {
		return result, err
	}
	status, err := command(ctx, dir, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	// command sets GOWORK=off and an empty GOFLAGS for every build.
	if _, err = command(ctx, dir, "go", "build", "-mod=readonly", "-trimpath", "-o", binary, driver); err != nil {
		return result, err
	}
	afterHash, err := sourceHash(ctx, dir)
	if err != nil {
		return result, err
	}
	afterStatus, err := command(ctx, dir, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	if afterHash != result.SourceSHA256 || !bytes.Equal(status, afterStatus) {
		return result, fmt.Errorf("library checkout changed during build: %s", dir)
	}
	result.BinarySHA256, err = fileHash(binary)
	return result, err
}

// executableSuffix is the file extension Windows needs to run a built binary.
func executableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func extract(ctx context.Context, binary, method string, pdf []byte, timeout time.Duration) (icdar2013.DocumentExtraction, error) {
	workerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(workerCtx, binary, method)
	cmd.Stdin = bytes.NewReader(pdf)
	var stdout, stderr cappedBuffer
	stdout.limit, stderr.limit = 32<<20, 1<<20
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Do not wait for output pipes once the worker has exited or been killed.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return icdar2013.DocumentExtraction{}, fmt.Errorf("whole benchmark cancelled: %w", ctx.Err())
		}
		if workerCtx.Err() != nil {
			return icdar2013.DocumentExtraction{Error: "worker timeout/cancelled"}, nil
		}
		return icdar2013.DocumentExtraction{Error: fmt.Sprintf("worker process failed: %v", err)}, nil
	}
	return icdar2013.ParseDocumentExtraction(stdout.Bytes())
}

func scoreCorpus(ctx context.Context, binary, label string, timeout time.Duration, index icdar2013.DocumentIndex, pdfs map[string][]byte, truth map[string][][][]string) ([]icdar2013.DocumentCaseResult, error) {
	cases := make([]icdar2013.DocumentCaseResult, 0, len(index.Cases)*2)
	for _, method := range []string{icdar2013.DocumentMethodStream, icdar2013.DocumentMethodAuto} {
		cache := make(map[string]icdar2013.DocumentExtraction)
		for _, input := range index.Cases {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("whole benchmark timeout: %w", ctx.Err())
			}
			actual, ok := cache[input.PDF]
			if !ok {
				var err error
				actual, err = extract(ctx, binary, method, pdfs[input.PDF], timeout)
				if err != nil {
					return nil, fmt.Errorf("%s %s %s invalid worker JSON: %w", label, method, input.ID, err)
				}
				cache[input.PDF] = actual
				fmt.Fprintf(os.Stderr, "%s %s %s: tables=%d error=%q\n", label, method, input.ID, len(actual.Tables), actual.Error)
			}
			cases = append(cases, icdar2013.DocumentCaseResult{Input: input, Method: method, Score: icdar2013.CompareDocuments(truth[input.ID], actual.Tables), Tables: len(actual.Tables), Error: actual.Error})
		}
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("whole benchmark cancelled: %w", ctx.Err())
	}
	return cases, nil
}

func run() error {
	root := flag.String("corpus", "", "pinned tabula-java icdar2013-dataset directory")
	baseDir := flag.String("baseline-dir", "", "untouched baseline module checkout")
	headDir := flag.String("head-dir", ".", "head module checkout")
	binDir := flag.String("bin-dir", "", "existing directory for baseline/head worker binaries")
	outDir := flag.String("output-dir", "", "existing directory for deterministic reports")
	timeout := flag.Duration("pdf-timeout", 30*time.Second, "wall-clock timeout per unique PDF/method")
	wall := flag.Duration("wall-timeout", 30*time.Minute, "whole process/build/extraction limit")
	flag.Parse()
	if *root == "" || *baseDir == "" || *binDir == "" || *outDir == "" || *timeout <= 0 || *wall <= 0 {
		return fmt.Errorf("corpus, baseline-dir, bin-dir and output-dir are required; timeouts must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wall)
	defer cancel()
	// Enforce a process-level deadline even during bounded conversion/scoring,
	// which does not spawn a cancellable extraction worker.
	deadline := time.AfterFunc(*wall, func() { fmt.Fprintln(os.Stderr, "whole benchmark wall-clock deadline exceeded"); os.Exit(1) })
	defer deadline.Stop()
	index, pdfs, truth, err := icdar2013.LoadDocumentInputs(*root)
	if err != nil {
		return err
	}
	headAbs, err := filepath.Abs(*headDir)
	if err != nil {
		return err
	}
	driver := filepath.Join(headAbs, "testdata", "icdar2013", "runner", "main.go")
	driverHash, err := fileHash(driver)
	if err != nil {
		return err
	}
	version, err := command(ctx, headAbs, "go", "version")
	if err != nil {
		return err
	}
	var tooling strings.Builder
	for _, name := range []string{"internal/benchmark/icdar2013/document.go", "internal/benchmark/icdar2013/scorer.go", "internal/benchmark/icdar2013/corpus.go", "internal/benchmark/icdar2013/document-index.json", "cmd/icdar2013-corpus/main.go", "testdata/icdar2013/runner/main.go"} {
		digest, readErr := fileHash(filepath.Join(headAbs, name))
		if readErr != nil {
			return readErr
		}
		fmt.Fprintf(&tooling, "%s\x00%s\n", name, digest)
	}
	reports := make([]icdar2013.DocumentReport, 0, 2)
	for i, dir := range []string{*baseDir, headAbs} {
		label := []string{"baseline", "head"}[i]
		binary, absErr := filepath.Abs(filepath.Join(*binDir, label+"-runner"+executableSuffix()))
		if absErr != nil {
			return absErr
		}
		provenance, buildErr := build(ctx, dir, driver, binary)
		if buildErr != nil {
			return buildErr
		}
		report := icdar2013.DocumentReport{SchemaVersion: icdar2013.DocumentSchema, Label: icdar2013.DocumentLabel, IndexSHA256: icdar2013.DocumentIndexSHA256, SourceCommit: icdar2013.TabulaJavaCommit, Normalization: icdar2013.NormalizationMethod, Scoring: icdar2013.ScoringMethod, Convention: icdar2013.DocumentConvention, DriverSHA256: driverHash, ToolingSHA256: hash([]byte(tooling.String())), GoVersion: strings.TrimSpace(string(version)), Build: provenance, AnnotationSets: len(index.Cases), UniquePDFs: len(pdfs)}
		report.Cases, err = scoreCorpus(ctx, binary, label, *timeout, index, pdfs, truth)
		if err != nil {
			return err
		}
		report.Aggregate = icdar2013.AggregateDocuments(report.Cases)
		if err := icdar2013.ValidateDocumentReport(&report); err != nil {
			return err
		}
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		if err := os.WriteFile(filepath.Join(*outDir, label+".json"), append(data, '\n'), 0o644); err != nil {
			return err
		}
		reports = append(reports, report)
		fmt.Fprintf(os.Stderr, "%s aggregate: %+v\n", label, report.Aggregate)
	}
	return icdar2013.GateDocumentReports(&reports[0], &reports[1])
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
