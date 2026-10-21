package icdar2013

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDocumentXMLAnchorProjection(t *testing.T) {
	data := `<document><table id="1"><region page="1" row-increment="-1" col-increment="-2">
<cell id="1" start-row="1" start-col="2" end-row="2" end-col="3"><content> MERGED
Header </content></cell>
<cell id="1" start-row="1" start-col="4"><content>B</content></cell>
<cell id="1" start-row="2" start-col="4"><content>C</content></cell>
<cell id="1" start-row="3" start-col="2"><content>D</content></cell>
</region><region page="2"><cell start-row="8" start-col="7"><content>X</content></cell><cell start-row="8" start-col="8"><content>Y</content></cell></region></table>
<table id="1"><region page="1"><cell start-row="0" start-col="0"><content>X</content></cell><cell start-row="0" start-col="1"><content>Y</content></cell></region></table></document>`
	matrices, err := ParseDocumentXML([]byte(data))
	require.NoError(t, err)
	require.Len(t, matrices, 3)
	require.Equal(t, [][]string{{" MERGED\nHeader ", "", "B"}, {"", "", "C"}, {"D", "", ""}}, matrices[0])
	relations := documentRelations(matrices)
	require.Equal(t, 2, relations[relationKey{"right", "x", "y"}], "independent tables preserve multiplicity")
	require.Equal(t, 1, relations[relationKey{"below", "merged header", "d"}], "covered cells remain empty")
	require.Equal(t, 0, relations[relationKey{"below", "d", "x"}], "no cross-region relation")
	score := CompareDocuments(matrices, matrices[1:])
	require.Equal(t, 5, score.Expected)
	require.Equal(t, 2, score.Actual)
	require.Equal(t, 2, score.Matched)
	require.Equal(t, 2, CompareDocuments(matrices[1:], matrices[1:2]).Expected)
	require.Equal(t, 1, CompareDocuments(matrices[1:], matrices[1:2]).Matched)
}

func TestDocumentXMLRejectsInvalidAndUnbounded(t *testing.T) {
	validCell := `<cell start-row="0" start-col="0"><content>A</content></cell>`
	tests := []string{
		`<document/>`, `<other/>`, `<document><table/></document>`,
		`<document><table><region page="0">` + validCell + `</region></table></document>`,
		`<document><table><region page="1">` + validCell + validCell + `</region></table></document>`,
		`<document><table><region page="1"><cell start-row="2" start-col="0" end-row="1"/></region></table></document>`,
		`<document><table><region page="1"><cell start-row="0" start-col="0" end-row="99999999"/></region></table></document>`,
		`<document><table><region page="1"><cell start-col="0"/></region></table></document>`,
		`<document><table><region page="1"><cell start-row="0" start-col="0" end-row="1024" end-col="1024"/></region></table></document>`,
		`<!DOCTYPE document><document/>`, `<document/><document/>`,
		strings.Repeat(`<document>`, 17) + strings.Repeat(`</document>`, 17),
	}
	for _, data := range tests {
		_, err := ParseDocumentXML([]byte(data))
		require.Error(t, err, data)
	}
}

func TestDocumentPinnedCoverageAndAliases(t *testing.T) {
	index, err := PinnedDocumentIndex()
	require.NoError(t, err)
	require.Len(t, index.Cases, 71)
	pdfs := make(map[string]bool)
	byID := make(map[string]DocumentInput)
	for _, input := range index.Cases {
		pdfs[input.PDF] = true
		byID[input.ID] = input
	}
	require.Len(t, pdfs, 67)
	for _, prefix := range []string{"eu-009", "us-011", "us-031", "us-035"} {
		a, b := byID[prefix+"a"], byID[prefix+"b"]
		require.Equal(t, a.PDF, b.PDF)
		require.Equal(t, a.PDFSHA256, b.PDFSHA256)
		require.NotEqual(t, a.XML, b.XML)
		if prefix != "us-035" {
			require.NotEqual(t, a.XMLSHA256, b.XMLSHA256)
		}
	}
	for _, data := range [][]byte{nil, append(append([]byte(nil), documentIndex...), '\n')} {
		_, err = ParseDocumentIndex(data)
		require.ErrorContains(t, err, "hash mismatch")
	}
	index.Cases = index.Cases[:70]
	data, err := json.Marshal(index)
	require.NoError(t, err)
	_, err = ParseDocumentIndex(data)
	require.Error(t, err)
	_, _, _, err = LoadDocumentInputs(t.TempDir())
	require.Error(t, err, "incomplete corpus must fail")
}

func TestDocumentAssetValidation(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "input.xml"), []byte("actual"), 0o600))
	file, err := resolveCorpusAsset(root, "input.xml")
	require.NoError(t, err)
	_, err = readVerifiedBytes(file, "case", "xml", strings.Repeat("0", 64), 32)
	require.ErrorContains(t, err, "hash mismatch")
	_, err = readVerifiedBytes(file, "case", "xml", fmt.Sprintf("%x", sha256.Sum256([]byte("actual"))), 5)
	require.ErrorContains(t, err, "exceeds 5-byte limit")
	data, err := readVerifiedBytes(file, "case", "xml", fmt.Sprintf("%x", sha256.Sum256([]byte("actual"))), 6)
	require.NoError(t, err)
	require.Equal(t, "actual", string(data))
	for _, name := range []string{"", "../input.xml", "sub/../../input.xml", ".", filepath.Join(root, "input.xml")} {
		_, err = resolveCorpusAsset(root, name)
		require.Error(t, err, name)
	}
	outside := filepath.Join(t.TempDir(), "outside.xml")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	if err := os.Symlink(outside, filepath.Join(root, "escape.xml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err) // e.g. Windows without the privilege
	}
	_, err = resolveCorpusAsset(root, "escape.xml")
	require.ErrorContains(t, err, "escapes")
}

func TestDocumentMissingAndErrorExtraction(t *testing.T) {
	expected := [][][]string{{{"A", "B"}}}
	for _, data := range []string{`{"tables":[]}`, `{"tables":null,"error":"failed extraction"}`} {
		actual, err := ParseDocumentExtraction([]byte(data))
		require.NoError(t, err)
		score := CompareDocuments(expected, actual.Tables)
		require.Equal(t, scoreFromCounts(1, 0, 0), score)
	}
	require.Equal(t, Score{}, CompareDocuments(nil, nil))
	for _, data := range []string{`{"tables":[],"unknown":0}`, `{"tables":[[["a"]]],"error":"failure"}`, `{} {}`, strings.Repeat("[", 17) + strings.Repeat("]", 17)} {
		_, err := ParseDocumentExtraction([]byte(data))
		require.Error(t, err)
	}
	data, err := json.Marshal(DocumentExtraction{Tables: [][][]string{make([][]string, 1025)}})
	require.NoError(t, err)
	_, err = ParseDocumentExtraction(data)
	require.Error(t, err)
}

func fullDocumentReport(t *testing.T) DocumentReport {
	t.Helper()
	index, err := PinnedDocumentIndex()
	require.NoError(t, err)
	digest := strings.Repeat("a", 64)
	report := DocumentReport{SchemaVersion: DocumentSchema, Label: DocumentLabel, IndexSHA256: DocumentIndexSHA256, SourceCommit: TabulaJavaCommit, Normalization: NormalizationMethod, Scoring: ScoringMethod, Convention: DocumentConvention, DriverSHA256: digest, ToolingSHA256: digest, GoVersion: "go version test", Build: DocumentBuild{Revision: strings.Repeat("a", 40), SourceSHA256: digest, BinarySHA256: digest}, AnnotationSets: 71, UniquePDFs: 67}
	for _, method := range []string{"Stream", "Auto"} {
		for i, input := range index.Cases {
			report.Cases = append(report.Cases, DocumentCaseResult{Input: input, Method: method, Score: scoreFromCounts(i+1, i+1, i+1), Tables: 1})
		}
	}
	report.Aggregate = AggregateDocuments(report.Cases)
	return report
}

func cloneDocumentReport(t *testing.T, report DocumentReport) DocumentReport {
	t.Helper()
	data, err := json.Marshal(report)
	require.NoError(t, err)
	var result DocumentReport
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func TestDocumentAggregationAndRegressionGate(t *testing.T) {
	base := fullDocumentReport(t)
	require.NoError(t, ValidateDocumentReport(&base))
	require.Equal(t, 2556, base.Aggregate["Stream"].Expected)
	require.Equal(t, 1.0, base.Aggregate["Stream"].F1)
	require.NoError(t, GateDocumentReports(&base, &base))
	for _, method := range []string{"Stream", "Auto"} {
		head := cloneDocumentReport(t, base)
		for i := range head.Cases {
			if head.Cases[i].Method == method {
				head.Cases[i].Score = scoreFromCounts(head.Cases[i].Score.Expected, 0, 0)
				head.Cases[i].Tables = 0
				break
			}
		}
		head.Aggregate = AggregateDocuments(head.Cases)
		require.NoError(t, ValidateDocumentReport(&head))
		require.ErrorContains(t, GateDocumentReports(&base, &head), method+" micro F1 regressed")
	}
	counts := AggregateDocuments([]DocumentCaseResult{{Method: "Stream", Score: scoreFromCounts(1, 1, 1)}, {Method: "Stream", Score: scoreFromCounts(99, 0, 0)}})
	require.Equal(t, scoreFromCounts(100, 1, 1), counts["Stream"], "micro F1, not macro F1")
}

func TestDocumentGateRejectsNewErrorsRegardlessOfAggregate(t *testing.T) {
	for _, offset := range []int{0, 71} {
		for _, failure := range []string{"content-stream parse error", "worker timeout/cancelled"} {
			base := fullDocumentReport(t)
			base.Cases[offset].Score = scoreFromCounts(base.Cases[offset].Score.Expected, 0, 0)
			base.Cases[offset].Tables = 0 // successful extraction with no predictions
			base.Cases[offset+1].Score = scoreFromCounts(base.Cases[offset+1].Score.Expected, 0, 0)
			base.Cases[offset+1].Tables = 0
			base.Aggregate = AggregateDocuments(base.Cases)
			for _, improve := range []bool{false, true} {
				head := cloneDocumentReport(t, base)
				head.Cases[offset].Error = failure
				if improve {
					n := head.Cases[offset+1].Score.Expected
					head.Cases[offset+1].Score = scoreFromCounts(n, n, n)
					head.Cases[offset+1].Tables = 1
				}
				head.Aggregate = AggregateDocuments(head.Cases)
				require.GreaterOrEqual(t, head.Aggregate[head.Cases[offset].Method].F1, base.Aggregate[head.Cases[offset].Method].F1)
				require.ErrorContains(t, GateDocumentReports(&base, &head), "extraction regressed from success to error")
				baseWithError := cloneDocumentReport(t, base)
				baseWithError.Cases[offset].Error = failure
				require.NoError(t, GateDocumentReports(&baseWithError, &head), "existing errors are allowed")
				head.Cases[offset].Error = ""
				require.NoError(t, GateDocumentReports(&baseWithError, &head), "error-to-success is allowed")
			}
		}
	}
}

func TestDocumentGateAllowsPerCaseF1DecreaseWithAggregateImprovement(t *testing.T) {
	base := fullDocumentReport(t)
	base.Cases[1].Score = scoreFromCounts(2, 0, 0)
	base.Cases[1].Tables = 0
	base.Aggregate = AggregateDocuments(base.Cases)
	head := cloneDocumentReport(t, base)
	head.Cases[0].Score = scoreFromCounts(1, 0, 0)
	head.Cases[0].Tables = 0
	head.Cases[1].Score = scoreFromCounts(2, 2, 2)
	head.Cases[1].Tables = 1
	head.Aggregate = AggregateDocuments(head.Cases)
	require.NoError(t, GateDocumentReports(&base, &head))
}

func TestDocumentReportRejectsFingerprintAndCoverageTamper(t *testing.T) {
	base := fullDocumentReport(t)
	mutations := []func(*DocumentReport){
		func(r *DocumentReport) { r.Cases = r.Cases[:141] },
		func(r *DocumentReport) { r.Cases[1] = r.Cases[0] },
		func(r *DocumentReport) { r.Cases[0].Input.PDF = "../escape.pdf" },
		func(r *DocumentReport) { r.Cases[0].Input.XMLSHA256 = strings.Repeat("b", 64) },
		func(r *DocumentReport) { r.Cases[0].Score.F1 = 0 },
		func(r *DocumentReport) { r.Aggregate["Stream"] = Score{} },
		func(r *DocumentReport) { r.AnnotationSets = 70 },
		func(r *DocumentReport) { r.Normalization = "other" },
		func(r *DocumentReport) { r.Cases[0].Error = "failed" },
	}
	for _, mutate := range mutations {
		head := cloneDocumentReport(t, base)
		mutate(&head)
		require.Error(t, GateDocumentReports(&base, &head))
	}
	head := cloneDocumentReport(t, base)
	head.DriverSHA256 = strings.Repeat("b", 64)
	require.ErrorContains(t, GateDocumentReports(&base, &head), "tooling fingerprint")
	head = cloneDocumentReport(t, base)
	head.Cases[0].Score = scoreFromCounts(2, 2, 2)
	head.Cases[1].Score = scoreFromCounts(1, 1, 1)
	head.Aggregate = AggregateDocuments(head.Cases)
	require.NoError(t, ValidateDocumentReport(&head))
	require.ErrorContains(t, GateDocumentReports(&base, &head), "per-case expected")
}

func TestDocumentExternalPinnedCorpus(t *testing.T) {
	root := os.Getenv("GXPDF_ICDAR2013_DOCUMENT_DIR")
	if root == "" {
		t.Skip("set GXPDF_ICDAR2013_DOCUMENT_DIR to validate all pinned XML/PDF inputs")
	}
	index, pdfs, truth, err := LoadDocumentInputs(root)
	require.NoError(t, err)
	require.Len(t, index.Cases, 71)
	require.Len(t, pdfs, 67)
	regions, relations := 0, 0
	for _, input := range index.Cases {
		regions += len(truth[input.ID])
		relations += CompareDocuments(truth[input.ID], nil).Expected
	}
	require.Equal(t, 167, regions)
	require.Equal(t, 25416, relations)
}
