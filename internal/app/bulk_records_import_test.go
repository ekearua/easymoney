package app

import (
	"strings"
	"testing"
)

func TestParseRecordsFileSkipsSerialOnlyRows(t *testing.T) {
	const file = `S/N,Name,Phone Number,BVN,NIN,VIN,Account Number,Bank Name,Account Name,State,LGA,Ward,Polling Unit/Booth,Source File
1,Ahmed Musa,07061234567,21234567,12345678890,1234545677888,2033334455,UBA,Ahmed Musa,Taraba,Jalingo,Ward 2,Polling unit 2,
2,,,,,,,,,,,,,
3,,,,,,,,,,,,,
`

	rows, problems := parseRecordsFile(strings.NewReader(file), "Records Template.csv", nil, "Records Template.csv")
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.PhoneE164 != "07061234567" || got.DisplayName != "Ahmed Musa" {
		t.Fatalf("phone/name = %q/%q", got.PhoneE164, got.DisplayName)
	}
	if got.BankCode != "" {
		t.Fatalf("BankCode = %q, want empty when the column is absent", got.BankCode)
	}
	if got.SourceFile != "Records Template.csv" {
		t.Fatalf("SourceFile = %q, want upload filename", got.SourceFile)
	}
}

func TestParseRecordsFileKeepsPresentBankCode(t *testing.T) {
	const file = `Phone Number,Bank Code,Name
07061234567,058,Ahmed Musa
`

	rows, problems := parseRecordsFile(strings.NewReader(file), "records.csv", nil, "records.csv")
	if len(problems) != 0 || len(rows) != 1 {
		t.Fatalf("rows/problems = %d/%v, want 1/none", len(rows), problems)
	}
	if rows[0].BankCode != "058" {
		t.Fatalf("BankCode = %q, want 058", rows[0].BankCode)
	}
}

func TestParseRecordsFileWarnsWhenRowHasDataButNoPhone(t *testing.T) {
	const file = `S/N,Name,Phone Number
1,Ahmed Musa,
2,,
3,,
`

	rows, problems := parseRecordsFile(strings.NewReader(file), "records.csv", nil, "records.csv")
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "no phone number") {
		t.Fatalf("problems = %v, want one missing-phone warning", problems)
	}
	if !strings.HasPrefix(problems[0], "1:") {
		t.Fatalf("problem = %q, want the S/N column to identify the row", problems[0])
	}
}

func TestParseRecordsFileLegacyTwoColumnUpload(t *testing.T) {
	const file = `07061234567,Ahmed Musa
08030000000,Ada Obi
`

	rows, problems := parseRecordsFile(strings.NewReader(file), "recipients.csv", nil, "recipients.csv")
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.PhoneE164 == "" || row.DisplayName == "" {
			t.Fatalf("legacy row not mapped: %+v", row)
		}
	}
}
