// This identical source is built against baseline and head from their own
// module directories. It intentionally imports no internal gxpdf package.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/coregx/gxpdf"
)

type extraction struct {
	Tables [][][]string `json:"tables"`
	Error  string       `json:"error,omitempty"`
}

func extract() extraction {
	if len(os.Args) != 2 {
		return extraction{Error: "expected Stream or Auto"}
	}
	method := gxpdf.MethodStream
	switch os.Args[1] {
	case "Stream":
	case "Auto":
		method = gxpdf.MethodAuto
	default:
		return extraction{Error: "invalid extraction method"}
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, (128<<20)+1))
	if err != nil {
		return extraction{Error: err.Error()}
	}
	if len(data) > 128<<20 {
		return extraction{Error: "PDF exceeds byte limit"}
	}
	doc, err := gxpdf.OpenFromBytes(data)
	if err != nil {
		return extraction{Error: fmt.Sprintf("open: %v", err)}
	}
	tables, extractionErr := doc.ExtractTablesWithOptions(gxpdf.DefaultExtractionOptions().WithMethod(method))
	closeErr := doc.Close()
	if extractionErr != nil {
		return extraction{Error: fmt.Sprintf("extract: %v", extractionErr)}
	}
	if closeErr != nil {
		return extraction{Error: fmt.Sprintf("close: %v", closeErr)}
	}
	result := extraction{Tables: make([][][]string, 0, len(tables))}
	if len(tables) > 4096 {
		return extraction{Error: "extracted table limit exceeded"}
	}
	cells := 0
	for _, table := range tables {
		if table.RowCount() > 1024 || table.ColumnCount() > 1024 {
			return extraction{Error: "extracted grid dimension limit exceeded"}
		}
		cells += table.RowCount() * table.ColumnCount()
		if cells > 200_000 {
			return extraction{Error: "extracted cell limit exceeded"}
		}
		result.Tables = append(result.Tables, table.Rows())
	}
	return result
}

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(extract()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
