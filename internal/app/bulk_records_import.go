package app

import (
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"whatsapp-payment-demo/internal/store"
)

// recordField maps a template column to a record field.
type recordField int

const (
	fieldIgnore recordField = iota
	fieldRow
	fieldName
	fieldPhone
	fieldBVN
	fieldNIN
	fieldVIN
	fieldAccountNumber
	fieldBankName
	fieldBankCode
	fieldAccountName
	fieldState
	fieldLGA
	fieldWard
	fieldPollingUnit
	fieldSourceFile
)

// recordHeaderAliases maps a normalized header cell to a field. The Records
// Template headers arrive with spaces and punctuation, so both the header and
// the lookup are normalized to lowercase letters and digits.
var recordHeaderAliases = map[string]recordField{
	"sn":               fieldRow,
	"sno":              fieldRow,
	"serial":           fieldRow,
	"row":              fieldRow,
	"name":             fieldName,
	"fullname":         fieldName,
	"recipient":        fieldName,
	"beneficiary":      fieldName,
	"phone":            fieldPhone,
	"phonenumber":      fieldPhone,
	"phonenumbers":     fieldPhone,
	"msisdn":           fieldPhone,
	"mobile":           fieldPhone,
	"telephone":        fieldPhone,
	"bvn":              fieldBVN,
	"nin":              fieldNIN,
	"vin":              fieldVIN,
	"voterid":          fieldVIN,
	"vinnumber":        fieldVIN,
	"accountnumber":    fieldAccountNumber,
	"acctnumber":       fieldAccountNumber,
	"accountno":        fieldAccountNumber,
	"bankname":         fieldBankName,
	"bank":             fieldBankName,
	"bankcode":         fieldBankCode,
	"accountname":      fieldAccountName,
	"state":            fieldState,
	"lga":              fieldLGA,
	"localgovernment":  fieldLGA,
	"ward":             fieldWard,
	"pollingunit":      fieldPollingUnit,
	"pollingunitbooth": fieldPollingUnit,
	"pollingbooth":     fieldPollingUnit,
	"booth":            fieldPollingUnit,
	"sourcefile":       fieldSourceFile,
}

func normalizeRecordHeader(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseRecordsFile reads phone/identity/geography records from a CSV or XLSX
// upload shaped like docs/user-scripts/Records Template.xlsx. Column order does
// not matter: the header row drives the mapping. Problems are returned as
// human-readable lines so the page can show why rows were skipped.
func parseRecordsFile(file io.Reader, filename string, centerID *uuid.UUID, sourceFile string) ([]store.BulkRecipientInput, []string) {
	var rows [][]string
	var err error
	if strings.HasSuffix(strings.ToLower(filename), ".xlsx") {
		rows, err = readXLSXRows(file)
	} else {
		rows, err = readCSVRows(file)
	}
	if err != nil {
		return nil, []string{"Could not read the file: " + err.Error()}
	}
	if len(rows) == 0 {
		return nil, []string{"The file has no rows."}
	}

	columns := map[recordField]int{}
	start := 0
	if index, ok := recordHeaderRow(rows[0]); ok {
		columns = index
		start = 1
	}

	var out []store.BulkRecipientInput
	var problems []string
	for i := start; i < len(rows); i++ {
		row := rows[i]
		if rowIsBlank(row) {
			continue
		}
		// Templates pre-fill unused rows with serial numbers. Those rows carry
		// no record data, so they are skipped instead of reported as missing a
		// phone number.
		if len(columns) > 0 && !rowHasRecordData(row, columns) {
			continue
		}
		input := storeBRI(row, columns, centerID, sourceFile)
		if strings.TrimSpace(input.PhoneE164) == "" {
			problems = append(problems, recordDataRowId(i, row, columns)+": no phone number")
			continue
		}
		out = append(out, input)
	}
	return out, problems
}

// recordHeaderRow returns the column index when the first row looks like a
// header, and false when it looks like data.
func recordHeaderRow(row []string) (map[recordField]int, bool) {
	index := map[recordField]int{}
	matched := 0
	for i, cellValue := range row {
		header := normalizeRecordHeader(cellValue)
		if header == "" {
			continue
		}
		field, ok := recordHeaderAliases[header]
		if !ok {
			continue
		}
		matched++
		if field == fieldIgnore {
			continue
		}
		if _, exists := index[field]; !exists {
			index[field] = i
		}
	}
	if matched == 0 || !hasField(index, fieldPhone) {
		return nil, false
	}
	return index, true
}

// recordDataRowId returns a stable human-readable row id when the template
// carries an explicit serial/number column, otherwise falls back to the
// one-based file position.
func recordDataRowId(rowIndex int, row []string, columns map[recordField]int) string {
	if rid, ok := columns[fieldRow]; ok && rid >= 0 && rid < len(row) && strings.TrimSpace(row[rid]) != "" {
		return strings.TrimSpace(row[rid])
	}
	return strconv.Itoa(rowIndex + 1)
}

func hasField(index map[recordField]int, field recordField) bool {
	_, ok := index[field]
	return ok
}

// legacyPhoneName keeps the original two-column contract working: the first
// phone-like cell is the number, the first other non-empty cell is the name.
func legacyPhoneName(row []string) (string, string) {
	phone, name := "", ""
	for _, value := range row {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if phone == "" && looksLikePhone(value) {
			phone = value
			continue
		}
		if name == "" && !looksLikePhone(value) && !isSerialNumber(value) {
			name = value
		}
	}
	return phone, name
}

func isSerialNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func cell(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

// fieldCell reads the column mapped to field. A field that is not present in
// the header yields "", not column A: map lookups default to 0 otherwise, so an
// absent Bank Code column would silently import the S/N value.
func fieldCell(row []string, columns map[recordField]int, field recordField) string {
	index, ok := columns[field]
	if !ok {
		return ""
	}
	return cell(row, index)
}

// rowHasRecordData reports whether the row holds anything in a column that maps
// to a record field. Ignored columns (S/N, serial) and the row-number column do
// not count, so a template whose unused rows carry serials is not mistaken for
// rows with data.
func rowHasRecordData(row []string, columns map[recordField]int) bool {
	for field, index := range columns {
		if field == fieldIgnore || field == fieldRow {
			continue
		}
		if cell(row, index) != "" {
			return true
		}
	}
	return false
}

func rowIsBlank(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func readCSVRows(file io.Reader) ([][]string, error) {
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func readXLSXRows(file io.Reader) ([][]string, error) {
	book, err := excelize.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer book.Close()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("workbook has no sheets")
	}
	return book.GetRows(sheets[0])
}

// storeBRI mirrors the template row into store.BulkRecipientInput and keeps
// the parsed header index so post-import error lines can still name the
// template's row/serial identity.
func storeBRI(row []string, columns map[recordField]int, centerID *uuid.UUID, sourceFile string) store.BulkRecipientInput {
	in := store.BulkRecipientInput{
		CenterID: centerID, Source: "upload", SourceFile: sourceFile,
	}
	if len(columns) > 0 {
		in.PhoneE164 = fieldCell(row, columns, fieldPhone)
		in.DisplayName = fieldCell(row, columns, fieldName)
		in.BVN = fieldCell(row, columns, fieldBVN)
		in.NIN = fieldCell(row, columns, fieldNIN)
		in.VIN = fieldCell(row, columns, fieldVIN)
		in.AccountNumber = fieldCell(row, columns, fieldAccountNumber)
		in.BankName = fieldCell(row, columns, fieldBankName)
		in.BankCode = fieldCell(row, columns, fieldBankCode)
		in.AccountName = fieldCell(row, columns, fieldAccountName)
		in.State = fieldCell(row, columns, fieldState)
		in.LGA = fieldCell(row, columns, fieldLGA)
		in.Ward = fieldCell(row, columns, fieldWard)
		in.PollingUnit = fieldCell(row, columns, fieldPollingUnit)
		if fromFile := fieldCell(row, columns, fieldSourceFile); fromFile != "" {
			in.SourceFile = fromFile
		}
	} else {
		phone, name := legacyPhoneName(row)
		in.PhoneE164 = phone
		in.DisplayName = name
	}
	return in
}
