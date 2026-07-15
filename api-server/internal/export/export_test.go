package export

import (
	"bytes"
	"strings"
	"testing"
)

func sampleTable() Table {
	return Table{
		Title:   "General Ledger",
		Headers: []string{"Account", "Debit", "Credit"},
		Rows: [][]string{
			{"Cash & Bank — MoMo", "2000", "0"},
			{"Package Sales Revenue", "0", "2000"},
		},
	}
}

func TestParseAndContentType(t *testing.T) {
	cases := map[string]struct {
		in   string
		want Format
		ct   string
	}{
		"csv default": {"", FormatCSV, "text/csv"},
		"xlsx":        {"xlsx", FormatXLSX, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		"excel alias": {"excel", FormatXLSX, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		"pdf":         {"pdf", FormatPDF, "application/pdf"},
		"unknown":     {"weird", FormatCSV, "text/csv"},
	}
	for name, c := range cases {
		if got := Parse(c.in); got != c.want {
			t.Errorf("%s: Parse(%q)=%q want %q", name, c.in, got, c.want)
		}
		if got := c.want.ContentType(); got != c.ct {
			t.Errorf("%s: ContentType=%q want %q", name, got, c.ct)
		}
	}
}

func TestEncodeCSV(t *testing.T) {
	data, err := Encode(sampleTable(), FormatCSV)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "Account,Debit,Credit") {
		t.Errorf("CSV missing header row: %q", s)
	}
	if !strings.Contains(s, "Package Sales Revenue,0,2000") {
		t.Errorf("CSV missing data row: %q", s)
	}
}

func TestEncodeXLSXAndPDFProduceValidBytes(t *testing.T) {
	xlsx, err := Encode(sampleTable(), FormatXLSX)
	if err != nil {
		t.Fatal(err)
	}
	// XLSX is a zip archive — must start with the PK signature.
	if !bytes.HasPrefix(xlsx, []byte("PK")) {
		t.Errorf("xlsx does not start with zip signature")
	}

	pdf, err := Encode(sampleTable(), FormatPDF)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF")) {
		t.Errorf("pdf does not start with %%PDF header")
	}
}

func TestEncodeEmptyTable(t *testing.T) {
	// No headers/rows must not panic in any encoder.
	for _, f := range []Format{FormatCSV, FormatXLSX, FormatPDF} {
		if _, err := Encode(Table{Title: "Empty"}, f); err != nil {
			t.Errorf("format %s on empty table: %v", f, err)
		}
	}
}
