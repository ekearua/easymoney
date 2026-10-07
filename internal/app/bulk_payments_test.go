package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestParseRecipientCSV(t *testing.T) {
	input := "phone,name\n+2348011111111,Ada\n08022222222,Chidi\n\n2348033333333\n"
	rows, err := parseRecipientFile(strings.NewReader(input), "recipients.csv")
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].phone != "+2348011111111" || rows[0].name != "Ada" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[2].phone != "2348033333333" || rows[2].name != "" {
		t.Errorf("row 2 = %+v", rows[2])
	}
}

func TestParseRecipientCSVWithoutHeader(t *testing.T) {
	rows, err := parseRecipientFile(strings.NewReader("+2348011111111,Ada\n"), "recipients.csv")
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 1 || rows[0].phone != "+2348011111111" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}

func TestParseRecipientXLSX(t *testing.T) {
	book := excelize.NewFile()
	sheet := book.GetSheetName(0)
	records := [][]interface{}{
		{"phone", "name"},
		{"+2348011111111", "Ada"},
		{"08022222222", "Chidi"},
	}
	for r, record := range records {
		for c, value := range record {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				t.Fatal(err)
			}
			if err := book.SetCellValue(sheet, cell, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	var buf bytes.Buffer
	if err := book.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	rows, err := parseRecipientFile(bytes.NewReader(buf.Bytes()), "recipients.xlsx")
	if err != nil {
		t.Fatalf("parse xlsx: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(rows), rows)
	}
	if rows[1].phone != "08022222222" || rows[1].name != "Chidi" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}