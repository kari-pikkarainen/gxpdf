package icdar2013

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

const (
	DocumentSchema       = 2
	DocumentMethodStream = "Stream"
	DocumentMethodAuto   = "Auto"
	DocumentIndexSHA256  = "f52c0c3b9458847b8b791fc57ba292876897a2447fa5d0f7e015e1e505b8d2f3"
	DocumentConvention   = "anchor-projection; independent-region/table relation multiset union; no page-table matching"
	DocumentLabel        = "71 annotation sets from pinned tabula-java ICDAR reference corpus; 67 PDFs; simplified anchor adjacency/no page-table matching, not official score"
	maxDocumentCells     = 200_000
	maxDocumentDimension = 1024
)

//go:embed document-index.json
var documentIndex []byte

// DocumentInput pins one complete annotation set, including repeated PDF aliases.
type DocumentInput struct {
	ID        string `json:"id"`
	PDF       string `json:"pdf"`
	PDFSHA256 string `json:"pdfSha256"`
	XML       string `json:"xml"`
	XMLSHA256 string `json:"xmlSha256"`
}

// DocumentIndex lists every annotation set with the SHA-256 of its PDF and XML.
type DocumentIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	SourceCommit  string          `json:"sourceCommit"`
	Cases         []DocumentInput `json:"cases"`
}

// PinnedDocumentIndex returns the complete immutable coverage index.
func PinnedDocumentIndex() (DocumentIndex, error) { return ParseDocumentIndex(documentIndex) }

// ParseDocumentIndex rejects even a well-formed partial or edited coverage index.
func ParseDocumentIndex(data []byte) (DocumentIndex, error) {
	if fmt.Sprintf("%x", sha256.Sum256(data)) != DocumentIndexSHA256 {
		return DocumentIndex{}, fmt.Errorf("document coverage index hash mismatch")
	}
	var index DocumentIndex
	if err := strictDocumentJSON(data, &index); err != nil {
		return index, err
	}
	if index.SchemaVersion != DocumentSchema || index.SourceCommit != TabulaJavaCommit || len(index.Cases) != 71 {
		return index, fmt.Errorf("invalid document coverage index")
	}
	return index, nil
}

// LoadDocumentInputs validates all 71 cases before any extraction starts. It
// returns the exact verified PDF bytes, deduplicated by pinned relative path.
func LoadDocumentInputs(root string) (DocumentIndex, map[string][]byte, map[string][][][]string, error) {
	index, err := PinnedDocumentIndex()
	if err != nil {
		return index, nil, nil, err
	}
	pdfs := make(map[string][]byte)
	truth := make(map[string][][][]string)
	totalPDFBytes := 0
	resolved, err := resolveCorpusRoot(root)
	if err != nil {
		return index, nil, nil, err
	}
	for _, entry := range index.Cases {
		if _, ok := pdfs[entry.PDF]; !ok {
			file, pathErr := resolveCorpusAsset(resolved, entry.PDF)
			if pathErr != nil {
				return index, nil, nil, pathErr
			}
			pdfs[entry.PDF], err = readVerifiedBytes(file, entry.ID, "pdf", entry.PDFSHA256, maxPDFAssetBytes)
			if err != nil {
				return index, nil, nil, err
			}
			totalPDFBytes += len(pdfs[entry.PDF])
			if totalPDFBytes > 512<<20 {
				return index, nil, nil, fmt.Errorf("corpus PDF bytes exceed limit")
			}
		}
		file, pathErr := resolveCorpusAsset(resolved, entry.XML)
		if pathErr != nil {
			return index, nil, nil, pathErr
		}
		data, readErr := readVerifiedBytes(file, entry.ID, "xml", entry.XMLSHA256, maxDocumentBytes)
		if readErr != nil {
			return index, nil, nil, readErr
		}
		truth[entry.ID], err = ParseDocumentXML(data)
		if err != nil {
			return index, nil, nil, fmt.Errorf("%s: %w", entry.ID, err)
		}
	}
	if len(pdfs) != 67 {
		return index, nil, nil, fmt.Errorf("expected 67 PDFs, got %d", len(pdfs))
	}
	return index, pdfs, truth, nil
}

type annotationDocument struct {
	XMLName xml.Name          `xml:"document"`
	Tables  []annotationTable `xml:"table"`
}
type annotationTable struct {
	Regions []annotationRegion `xml:"region"`
}
type annotationRegion struct {
	Page         string           `xml:"page,attr"`
	RowIncrement string           `xml:"row-increment,attr"`
	ColIncrement string           `xml:"col-increment,attr"`
	Cells        []annotationCell `xml:"cell"`
}
type annotationCell struct {
	StartRow string `xml:"start-row,attr"`
	StartCol string `xml:"start-col,attr"`
	EndRow   string `xml:"end-row,attr"`
	EndCol   string `xml:"end-col,attr"`
	Content  string `xml:"content"`
}

func validateAnnotationXML(data []byte) error {
	if len(data) > maxDocumentBytes {
		return fmt.Errorf("XML too large")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, tokens, roots := 0, 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		tokens++
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || value.Name.Local != "document" {
					return fmt.Errorf("XML must have one document root")
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return fmt.Errorf("text outside XML root")
			}
		case xml.Directive:
			return fmt.Errorf("XML directives unsupported")
		}
		if depth > 16 || tokens > 2_000_000 {
			return fmt.Errorf("XML structure exceeds limit")
		}
	}
	return nil
}

// ParseDocumentXML projects merged text exactly once at its start coordinate.
// Physical regions remain separate, regardless of repeated cell/table IDs.
func ParseDocumentXML(data []byte) ([][][]string, error) {
	if err := validateAnnotationXML(data); err != nil {
		return nil, err
	}
	var doc annotationDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Tables) == 0 {
		return nil, fmt.Errorf("XML has no tables")
	}
	var matrices [][][]string
	total := 0
	for _, table := range doc.Tables {
		if len(table.Regions) == 0 {
			return nil, fmt.Errorf("table has no regions")
		}
		for _, region := range table.Regions {
			matrix, err := regionMatrix(region)
			if err != nil {
				return nil, err
			}
			total += len(matrix) * len(matrix[0])
			if total > maxDocumentCells {
				return nil, fmt.Errorf("annotation grids exceed cell limit")
			}
			matrices = append(matrices, matrix)
		}
	}
	return matrices, nil
}

func annotationIndex(value string, optional bool) (int, error) {
	if value == "" && optional {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < -maxDocumentDimension || n > maxDocumentDimension {
		return 0, fmt.Errorf("invalid annotation index %q", value)
	}
	return n, nil
}

type annotationAnchor struct {
	row, col, endRow, endCol int
	text                     string
}

func cellAnchor(cell annotationCell, ri, ci int) (annotationAnchor, error) {
	var result annotationAnchor
	r, err := annotationIndex(cell.StartRow, false)
	if err != nil {
		return result, err
	}
	c, err := annotationIndex(cell.StartCol, false)
	if err != nil {
		return result, err
	}
	er, ec := r, c
	if cell.EndRow != "" {
		er, err = annotationIndex(cell.EndRow, false)
		if err != nil {
			return result, err
		}
	}
	if cell.EndCol != "" {
		ec, err = annotationIndex(cell.EndCol, false)
		if err != nil {
			return result, err
		}
	}
	if er < r || ec < c {
		return result, fmt.Errorf("reversed cell span")
	}
	return annotationAnchor{r + ri, c + ci, er + ri, ec + ci, cell.Content}, nil
}

func regionMatrix(region annotationRegion) ([][]string, error) {
	page, err := annotationIndex(region.Page, false)
	if err != nil || page < 1 {
		return nil, fmt.Errorf("invalid one-based region page %q", region.Page)
	}
	ri, err := annotationIndex(region.RowIncrement, true)
	if err != nil {
		return nil, err
	}
	ci, err := annotationIndex(region.ColIncrement, true)
	if err != nil {
		return nil, err
	}
	if len(region.Cells) == 0 || len(region.Cells) > maxDocumentCells {
		return nil, fmt.Errorf("invalid region cell count")
	}
	anchors := make([]annotationAnchor, 0, len(region.Cells))
	minRow, minCol, maxRow, maxCol := 2*maxDocumentDimension, 2*maxDocumentDimension, -2*maxDocumentDimension, -2*maxDocumentDimension
	for _, cell := range region.Cells {
		anchor, parseErr := cellAnchor(cell, ri, ci)
		if parseErr != nil {
			return nil, parseErr
		}
		minRow, minCol, maxRow, maxCol = min(minRow, anchor.row), min(minCol, anchor.col), max(maxRow, anchor.endRow), max(maxCol, anchor.endCol)
		anchors = append(anchors, anchor)
	}
	rows, cols := maxRow-minRow+1, maxCol-minCol+1
	if rows > maxDocumentDimension || cols > maxDocumentDimension || rows*cols > maxDocumentCells {
		return nil, fmt.Errorf("region grid exceeds limit")
	}
	matrix := make([][]string, rows)
	for row := range matrix {
		matrix[row] = make([]string, cols)
	}
	occupied := make(map[[2]int]bool)
	for _, cell := range anchors {
		key := [2]int{cell.row - minRow, cell.col - minCol}
		if occupied[key] {
			return nil, fmt.Errorf("duplicate cell anchor")
		}
		occupied[key] = true
		matrix[key[0]][key[1]] = cell.text
	}
	return matrix, nil
}

// CompareDocuments reuses the original relation generator and count scorer;
// region/table boundaries are never joined into artificial adjacency edges.
func CompareDocuments(expected, actual [][][]string) Score {
	expectedCounts, actualCounts := documentRelations(expected), documentRelations(actual)
	e, a, m := 0, 0, 0
	for key, count := range expectedCounts {
		e += count
		m += min(count, actualCounts[key])
	}
	for _, count := range actualCounts {
		a += count
	}
	return scoreFromCounts(e, a, m)
}

func documentRelations(matrices [][][]string) map[relationKey]int {
	counts := make(map[relationKey]int)
	for _, matrix := range matrices {
		for key, count := range relationCounts(Relations(matrix)) {
			counts[key] += count
		}
	}
	return counts
}

// DocumentExtraction is the public-API-only worker's bounded JSON protocol.
type DocumentExtraction struct {
	Tables [][][]string `json:"tables"`
	Error  string       `json:"error,omitempty"`
}

// ParseDocumentExtraction treats malformed worker JSON as a tooling error, not
// an extraction miss. An explicit extraction error always has empty actuals.
func ParseDocumentExtraction(data []byte) (DocumentExtraction, error) {
	var result DocumentExtraction
	if err := strictDocumentJSON(data, &result); err != nil {
		return result, err
	}
	total := 0
	if len(result.Tables) > 4096 {
		return result, fmt.Errorf("too many extracted tables")
	}
	for _, table := range result.Tables {
		if len(table) > maxDocumentDimension {
			return result, fmt.Errorf("too many extracted rows")
		}
		for _, row := range table {
			if len(row) > maxDocumentDimension {
				return result, fmt.Errorf("too many extracted columns")
			}
			total += len(row)
		}
	}
	if total > maxDocumentCells {
		return result, fmt.Errorf("extracted cells exceed limit")
	}
	if result.Error != "" && len(result.Tables) != 0 {
		return result, fmt.Errorf("extraction error must have empty tables")
	}
	return result, nil
}

func strictDocumentJSON(data []byte, target any) error {
	if len(data) > maxDocumentBytes {
		return fmt.Errorf("document JSON exceeds limit")
	}
	structure := json.NewDecoder(bytes.NewReader(data))
	depth, tokens := 0, 0
	for {
		token, err := structure.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		tokens++
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter == '[' || delimiter == '{' {
				depth++
			} else {
				depth--
			}
		}
		if depth > 16 || tokens > 2_000_000 {
			return fmt.Errorf("document JSON structure exceeds limit")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

// DocumentCaseResult retains every annotation and extraction failure.
type DocumentCaseResult struct {
	Input  DocumentInput `json:"input"`
	Method string        `json:"method"`
	Score  Score         `json:"score"`
	Tables int           `json:"tables"`
	Error  string        `json:"error,omitempty"`
}

// DocumentBuild identifies both the actual library source and shared driver.
type DocumentBuild struct {
	Revision     string `json:"revision"`
	SourceSHA256 string `json:"sourceSha256"`
	BinarySHA256 string `json:"binarySha256"`
}

// DocumentReport is deterministic: no wall-clock timings or temporary paths.
type DocumentReport struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	Label          string               `json:"label"`
	IndexSHA256    string               `json:"indexSha256"`
	SourceCommit   string               `json:"sourceCommit"`
	Normalization  string               `json:"normalization"`
	Scoring        string               `json:"scoring"`
	Convention     string               `json:"convention"`
	DriverSHA256   string               `json:"driverSha256"`
	ToolingSHA256  string               `json:"toolingSha256"`
	GoVersion      string               `json:"goVersion"`
	Build          DocumentBuild        `json:"build"`
	AnnotationSets int                  `json:"annotationSets"`
	UniquePDFs     int                  `json:"uniquePdfs"`
	Cases          []DocumentCaseResult `json:"cases"`
	Aggregate      map[string]Score     `json:"aggregate"`
}

// AggregateDocuments uses micro counts, not a mean of per-document F1 values.
func AggregateDocuments(cases []DocumentCaseResult) map[string]Score {
	result := make(map[string]Score)
	for i := range cases {
		entry := &cases[i]
		s := result[entry.Method]
		s.Expected += entry.Score.Expected
		s.Actual += entry.Score.Actual
		s.Matched += entry.Score.Matched
		result[entry.Method] = s
	}
	for method, s := range result {
		result[method] = scoreFromCounts(s.Expected, s.Actual, s.Matched)
	}
	return result
}

func validateDocumentProvenance(report *DocumentReport) error {
	if report.SchemaVersion != DocumentSchema || report.IndexSHA256 != DocumentIndexSHA256 || report.SourceCommit != TabulaJavaCommit || report.Normalization != NormalizationMethod || report.Scoring != ScoringMethod || report.Convention != DocumentConvention || report.Label != DocumentLabel || report.AnnotationSets != 71 || report.UniquePDFs != 67 {
		return fmt.Errorf("document report fingerprint differs")
	}
	for _, hash := range []string{report.DriverSHA256, report.ToolingSHA256, report.Build.SourceSHA256, report.Build.BinarySHA256} {
		if !validSHA256(hash) {
			return fmt.Errorf("incomplete document provenance")
		}
	}
	if len(report.Build.Revision) != 40 || report.GoVersion == "" {
		return fmt.Errorf("incomplete build provenance")
	}
	return nil
}

// ValidateDocumentReport checks complete coverage, fingerprints, and counts.
func ValidateDocumentReport(report *DocumentReport) error {
	if err := validateDocumentProvenance(report); err != nil {
		return err
	}
	index, err := PinnedDocumentIndex()
	if err != nil {
		return err
	}
	if len(report.Cases) != 142 {
		return fmt.Errorf("incomplete document report coverage")
	}
	expected := make(map[string]DocumentInput)
	for _, input := range index.Cases {
		for _, method := range []string{DocumentMethodStream, DocumentMethodAuto} {
			expected[input.ID+"/"+method] = input
		}
	}
	for i := range report.Cases {
		result := &report.Cases[i]
		key := result.Input.ID + "/" + result.Method
		input, ok := expected[key]
		if !ok || input != result.Input {
			return fmt.Errorf("case fingerprint/coverage differs: %s", key)
		}
		delete(expected, key)
		if err := validateScore(result.Score); err != nil {
			return err
		}
		if result.Tables < 0 || (result.Error != "" && (result.Tables != 0 || result.Score.Actual != 0)) {
			return fmt.Errorf("invalid extraction failure")
		}
	}
	if !reflect.DeepEqual(AggregateDocuments(report.Cases), report.Aggregate) {
		return fmt.Errorf("aggregate counts differ")
	}
	return nil
}

// GateDocumentReports rejects mismatched inputs, new extraction failures, and
// regressions in either micro F1. Per-case F1 changes remain informational.
func GateDocumentReports(base, head *DocumentReport) error {
	if err := ValidateDocumentReport(base); err != nil {
		return err
	}
	if err := ValidateDocumentReport(head); err != nil {
		return err
	}
	if base.DriverSHA256 != head.DriverSHA256 || base.ToolingSHA256 != head.ToolingSHA256 || base.GoVersion != head.GoVersion {
		return fmt.Errorf("document tooling fingerprint differs")
	}
	baseExpected := make(map[string]int, len(base.Cases))
	baseErrors := make(map[string]string, len(base.Cases))
	for i := range base.Cases {
		result := &base.Cases[i]
		baseExpected[result.Input.ID+"/"+result.Method] = result.Score.Expected
		baseErrors[result.Input.ID+"/"+result.Method] = result.Error
	}
	for i := range head.Cases {
		result := &head.Cases[i]
		if result.Score.Expected != baseExpected[result.Input.ID+"/"+result.Method] {
			return fmt.Errorf("per-case expected relation counts differ")
		}
		if result.Error != "" && baseErrors[result.Input.ID+"/"+result.Method] == "" {
			return fmt.Errorf("%s %s extraction regressed from success to error: %s", result.Method, result.Input.ID, result.Error)
		}
	}
	for _, method := range []string{DocumentMethodStream, DocumentMethodAuto} {
		if base.Aggregate[method].Expected != head.Aggregate[method].Expected {
			return fmt.Errorf("expected relation counts differ")
		}
		if head.Aggregate[method].F1+scoreTolerance < base.Aggregate[method].F1 {
			return fmt.Errorf("%s micro F1 regressed: baseline %.15g head %.15g", method, base.Aggregate[method].F1, head.Aggregate[method].F1)
		}
	}
	return nil
}
