package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coregx/gxpdf/internal/benchmark/icdar2013"
	"github.com/stretchr/testify/require"
)

// Worker tests run this test binary as the extraction worker, so they need no
// shell and run on every platform. TestMain acts as the worker selected by
// testWorkerEnv instead of running the tests.
const (
	testWorkerEnv      = "ICDAR2013_CORPUS_TEST_WORKER"
	testWorkerCallsEnv = "ICDAR2013_CORPUS_TEST_WORKER_CALLS"
	// testWorkerTimeout leaves room for a slow first launch of the test
	// binary (e.g. antivirus scanning on Windows) where timing is not tested.
	testWorkerTimeout = 10 * time.Second
)

func TestMain(m *testing.M) {
	switch os.Getenv(testWorkerEnv) {
	case "":
		os.Exit(m.Run())
	case "sleep":
		time.Sleep(10 * time.Second)
	case "exit7":
		os.Exit(7)
	case "tables":
		calls, err := os.OpenFile(os.Getenv(testWorkerCallsEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, err = calls.WriteString("x")
		if closeErr := calls.Close(); err != nil || closeErr != nil {
			os.Exit(2)
		}
		fmt.Print(`{"tables":[[["A","B"]],[["A","B"]]]}`)
	case "malformed":
		fmt.Print("not json")
	}
	os.Exit(0)
}

// testWorker returns this test binary as a worker with the given behavior.
func testWorker(t *testing.T, behavior string) string {
	t.Helper()
	t.Setenv(testWorkerEnv, behavior)
	worker, err := os.Executable()
	require.NoError(t, err)
	return worker
}

func TestWorkerTimeoutAndFailureRemainExplicit(t *testing.T) {
	result, err := extract(context.Background(), testWorker(t, "sleep"), "Stream", nil, 20*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, "worker timeout/cancelled", result.Error)
	require.Empty(t, result.Tables)
	result, err = extract(context.Background(), testWorker(t, "exit7"), "Auto", nil, testWorkerTimeout)
	require.NoError(t, err)
	require.Contains(t, result.Error, "exit status 7")
	require.Empty(t, result.Tables)
}

func TestParentCancellationHardFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := extract(ctx, testWorker(t, "exit7"), "Stream", nil, time.Second)
	require.ErrorContains(t, err, "whole benchmark cancelled")
	require.Empty(t, result.Tables)
	_, err = scoreCorpus(ctx, "unused", "test", time.Second, icdar2013.DocumentIndex{}, nil, nil)
	require.ErrorContains(t, err, "whole benchmark cancelled")
}

func TestBoundedProvenanceHash(t *testing.T) {
	name := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(name, []byte("abc"), 0o600))
	digest, err := boundedFileHash(name, 3)
	require.NoError(t, err)
	require.Equal(t, hash([]byte("abc")), digest)
	_, err = boundedFileHash(name, 2)
	require.ErrorContains(t, err, "exceeds limit")
}

func TestCorpusWorkerCachePreservesAnnotationVariants(t *testing.T) {
	count := filepath.Join(t.TempDir(), "calls")
	worker := testWorker(t, "tables")
	t.Setenv(testWorkerCallsEnv, count)
	index := icdar2013.DocumentIndex{Cases: []icdar2013.DocumentInput{{ID: "a", PDF: "same.pdf"}, {ID: "b", PDF: "same.pdf"}, {ID: "c", PDF: "other.pdf"}}}
	truth := map[string][][][]string{"a": {{{"A", "B"}}}, "b": {{{"A", "B"}}, {{"A", "B"}}}, "c": nil}
	cases, err := scoreCorpus(context.Background(), worker, "test", testWorkerTimeout, index, map[string][]byte{"same.pdf": nil, "other.pdf": nil}, truth)
	require.NoError(t, err)
	require.Len(t, cases, 6)
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Len(t, data, 4, "once per unique PDF/method, never once per alias")
	require.Equal(t, 1, cases[0].Score.Matched)
	require.Equal(t, 2, cases[1].Score.Matched)
	require.Equal(t, 2, cases[2].Score.Actual, "all predicted tables contribute false positives")
}

func TestMalformedWorkerOutputHardFails(t *testing.T) {
	_, err := extract(context.Background(), testWorker(t, "malformed"), "Stream", nil, testWorkerTimeout)
	require.Error(t, err)
	buffer := cappedBuffer{limit: 2}
	_, err = buffer.Write([]byte("abc"))
	require.Error(t, err)
	require.Zero(t, buffer.Len())
}
