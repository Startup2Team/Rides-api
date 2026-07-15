// Package export turns a simple tabular model into CSV, XLSX, or PDF bytes so
// finance/admin reports can be downloaded in any of those formats from one data
// source.
package export

import (
	"bytes"
	"encoding/csv"
	"fmt"

	"github.com/go-pdf/fpdf"
	"github.com/xuri/excelize/v2"
)

// Format is a supported export encoding.
type Format string

const (
	FormatCSV  Format = "csv"
	FormatXLSX Format = "xlsx"
	FormatPDF  Format = "pdf"
)

// Parse normalises a user-supplied format string, defaulting to CSV.
func Parse(s string) Format {
	switch s {
	case "xlsx", "excel":
		return FormatXLSX
	case "pdf":
		return FormatPDF
	default:
		return FormatCSV
	}
}

// ContentType returns the HTTP content type for a format.
func (f Format) ContentType() string {
	switch f {
	case FormatXLSX:
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case FormatPDF:
		return "application/pdf"
	default:
		return "text/csv"
	}
}

// Ext returns the file extension (no dot) for a format.
func (f Format) Ext() string { return string(f) }

// Table is a format-neutral report: an optional title, a header row, and body
// rows. All cells are pre-formatted strings.
type Table struct {
	Title   string
	Headers []string
	Rows    [][]string
}

// Encode renders the table in the requested format.
func Encode(t Table, f Format) ([]byte, error) {
	switch f {
	case FormatXLSX:
		return encodeXLSX(t)
	case FormatPDF:
		return encodePDF(t)
	default:
		return encodeCSV(t)
	}
}

func encodeCSV(t Table) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if len(t.Headers) > 0 {
		if err := w.Write(t.Headers); err != nil {
			return nil, err
		}
	}
	for _, row := range t.Rows {
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeXLSX(t Table) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	sheet := "Sheet1"
	rowIdx := 1
	if t.Title != "" {
		cell, _ := excelize.CoordinatesToCellName(1, rowIdx)
		_ = f.SetCellValue(sheet, cell, t.Title)
		rowIdx += 2 // title, then a blank spacer row
	}
	if len(t.Headers) > 0 {
		for c, h := range t.Headers {
			cell, _ := excelize.CoordinatesToCellName(c+1, rowIdx)
			_ = f.SetCellValue(sheet, cell, h)
		}
		rowIdx++
	}
	for _, row := range t.Rows {
		for c, v := range row {
			cell, _ := excelize.CoordinatesToCellName(c+1, rowIdx)
			_ = f.SetCellValue(sheet, cell, v)
		}
		rowIdx++
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, fmt.Errorf("export: xlsx write: %w", err)
	}
	return buf.Bytes(), nil
}

func encodePDF(t Table) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "") // landscape fits wide financial tables
	pdf.SetMargins(10, 12, 10)
	pdf.AddPage()

	if t.Title != "" {
		pdf.SetFont("Helvetica", "B", 14)
		pdf.CellFormat(0, 8, t.Title, "", 1, "L", false, 0, "")
		pdf.Ln(2)
	}

	cols := len(t.Headers)
	if cols == 0 {
		for _, r := range t.Rows {
			if len(r) > cols {
				cols = len(r)
			}
		}
	}
	if cols == 0 {
		return pdfBytes(pdf)
	}

	// Distribute the usable page width evenly across columns.
	pageW, _ := pdf.GetPageSize()
	lm, _, rm, _ := pdf.GetMargins()
	colW := (pageW - lm - rm) / float64(cols)

	if len(t.Headers) > 0 {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetFillColor(230, 230, 230)
		for _, h := range t.Headers {
			pdf.CellFormat(colW, 7, h, "1", 0, "L", true, 0, "")
		}
		pdf.Ln(-1)
	}

	pdf.SetFont("Helvetica", "", 8)
	for _, row := range t.Rows {
		for c := 0; c < cols; c++ {
			val := ""
			if c < len(row) {
				val = truncate(row[c], 40)
			}
			pdf.CellFormat(colW, 6, val, "1", 0, "L", false, 0, "")
		}
		pdf.Ln(-1)
	}

	return pdfBytes(pdf)
}

func pdfBytes(pdf *fpdf.Fpdf) ([]byte, error) {
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("export: pdf write: %w", err)
	}
	return buf.Bytes(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
