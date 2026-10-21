# ICDAR 2013 table benchmark

`cmd/icdar2013-corpus` answers one question: does a change make gxpdf's table
extraction worse on the ICDAR 2013 table competition corpus?

It builds the same small extraction program (`runner/main.go`, public API
only) against two checkouts of gxpdf, a baseline and a head. Each program
extracts tables from all 67 PDFs with `MethodStream` and `MethodAuto`. The
results are scored against the 71 ground-truth annotation files, and the
command exits with an error if the head is worse than the baseline.

## Get the corpus

The PDFs and annotations are not stored in this repository. Use the copy in
tabula-java at a fixed commit:

```sh
curl --fail --location --output tabula-java.tar.gz \
  https://github.com/tabulapdf/tabula-java/archive/2cdf3b4fd3f7e921dca8cc6814cdd9316be40f0f.tar.gz
tar -xzf tabula-java.tar.gz
```

The corpus directory is then
`tabula-java-2cdf3b4fd3f7e921dca8cc6814cdd9316be40f0f/src/test/resources/technology/tabula/icdar2013-dataset`.

`internal/benchmark/icdar2013/document-index.json` lists every PDF and
annotation file with its SHA-256. The command checks all of them before it
starts, so a missing or different file is reported instead of being scored.

## Run it

You need Go and Git. The baseline is any checkout of gxpdf, for example a
`git worktree` of `main`. Run from the head checkout:

```sh
mkdir -p /path/to/bin /path/to/reports
go run ./cmd/icdar2013-corpus \
  -corpus /path/to/icdar2013-dataset \
  -baseline-dir /path/to/gxpdf-main \
  -head-dir . \
  -bin-dir /path/to/bin \
  -output-dir /path/to/reports
```

A full run takes under a minute on a laptop. It writes `baseline.json` and
`head.json` to the output directory, prints the Stream and Auto totals, and
exits with a non-zero status if:

- the Stream or Auto F1 of the head is lower than the baseline, or
- a document that the baseline extracted without error now fails (an error,
  crash, or timeout), even if the total F1 went up.

Documents that already failed on the baseline, and per-document F1 changes,
are reported but do not fail the run. Other options: `-pdf-timeout` (default
30s per PDF and method) and `-wall-timeout` (default 30m for the whole run).

Reports contain no timings or temporary paths, so two runs of the same code
produce identical files. They record the Git revision and a hash of the Go
sources of each checkout, the hash of the built program, and the Go version.

## How scoring works

Each annotated table region and each extracted table becomes a grid of cell
texts (`Table.Rows()` for extracted tables). Text is lowercased and runs of
whitespace are collapsed to one space; nothing else is changed. For every
non-empty cell the scorer records its nearest non-empty neighbour to the right
and below. Precision, recall and F1 compare these relations between the
annotation and the extraction, counting duplicates. Totals are summed over all
71 annotation files before F1 is computed.

Extraction runs once per PDF and method; the four PDFs with two annotation
files (`eu-009`, `us-011`, `us-031`, `us-035`) are scored against both.

## Limitations

- This is a simplified adjacency score, not the official ICDAR 2013 score.
  Extracted tables are not matched to annotated tables or pages, and merged
  cells count only at their top-left position.
- The numbers are useful for comparing two versions of gxpdf on the same
  corpus, not for comparing with results published elsewhere.
