package service

import "testing"

const (
	testMinKobo int64 = 10_000
	testMaxKobo int64 = 10_000_000
)

func TestThriftCommandParsing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		fn    func(string) (string, bool)
		want  string
		ok    bool
	}{
		{name: "join command", input: "JOIN XG-THRIFT-8K2Q", fn: thriftJoinCodeFromInput, want: "XG-THRIFT-8K2Q", ok: true},
		{name: "bare invite", input: "xg-thrift-8k2q", fn: thriftJoinCodeFromInput, want: "XG-THRIFT-8K2Q", ok: true},
		{name: "activate command", input: "activate XG-THRIFT-ABCD1234", fn: thriftActivateCodeFromInput, want: "XG-THRIFT-ABCD1234", ok: true},
		{name: "contribute command", input: "CONTRIBUTE XG-THRIFT-ABCD1234", fn: thriftContributeCodeFromInput, want: "XG-THRIFT-ABCD1234", ok: true},
		{name: "wrong verb", input: "PAY XG-THRIFT-ABCD1234", fn: thriftContributeCodeFromInput, ok: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got, ok := test.fn(test.input)
			if ok != test.ok || got != test.want {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestParseRotationIndexes(t *testing.T) {
	t.Parallel()
	got := parseRotationIndexes("2, 1 3")
	want := []int{2, 1, 3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if got := parseRotationIndexes("1 two 3"); got != nil {
		t.Fatalf("invalid input should return nil, got %v", got)
	}
}

func TestParseCommaSeparatedFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "empty", input: "", want: []string{}},
		{name: "single", input: "hello", want: []string{"hello"}},
		{name: "multiple", input: "a, b, c", want: []string{"a", "b", "c"}},
		{name: "extra whitespace", input: "  a ,  b  ,  c  ", want: []string{"a", "b", "c"}},
		{name: "empty between commas", input: "a,,b,,c", want: []string{"a", "b", "c"}},
		{name: "trailing comma", input: "a,b,c,", want: []string{"a", "b", "c"}},
		{name: "leading comma", input: ",a,b,c", want: []string{"a", "b", "c"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got := parseCommaSeparatedFields(test.input)
			if len(got) != len(test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
			for i := range test.want {
				if got[i] != test.want[i] {
					t.Fatalf("got %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestParseThriftConcatInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		input      string
		wantName   string
		wantAmount string
		wantFreq   string
		wantTarget string
		wantErrors int
	}{
		{name: "all four fields", input: "My Group, 5000, monthly, 8", wantName: "My Group", wantAmount: "5000", wantFreq: "monthly", wantTarget: "8", wantErrors: 0},
		{name: "all four reversed order", input: "8, My Group, 5000, monthly", wantName: "My Group", wantAmount: "5000", wantFreq: "monthly", wantTarget: "8", wantErrors: 0},
		{name: "all four random order", input: "monthly, 8, My Group, 5000", wantName: "My Group", wantAmount: "5000", wantFreq: "monthly", wantTarget: "8", wantErrors: 0},
		{name: "name and amount only", input: "My Group, 5000", wantName: "My Group", wantAmount: "5000", wantFreq: "", wantTarget: "", wantErrors: 0},
		{name: "name and frequency only", input: "My Group, weekly", wantName: "My Group", wantAmount: "", wantFreq: "weekly", wantTarget: "", wantErrors: 0},
		{name: "name and target only", input: "My Group, 6", wantName: "My Group", wantAmount: "", wantFreq: "", wantTarget: "6", wantErrors: 0},
		{name: "name only", input: "My Group", wantName: "My Group", wantAmount: "", wantFreq: "", wantTarget: "", wantErrors: 0},
		{name: "case insensitive frequency", input: "My Group, 5000, Monthly, 8", wantName: "My Group", wantAmount: "5000", wantFreq: "monthly", wantTarget: "8", wantErrors: 0},
		{name: "amount too low", input: "My Group, 5, monthly, 8", wantName: "My Group", wantAmount: "", wantFreq: "monthly", wantTarget: "", wantErrors: 0},
		{name: "invalid frequency", input: "My Group, 5000, daily, 8", wantName: "My Group", wantAmount: "5000", wantFreq: "", wantTarget: "8", wantErrors: 0},
		{name: "target out of range", input: "My Group, 5000, monthly, 15", wantName: "My Group", wantAmount: "5000", wantFreq: "monthly", wantTarget: "", wantErrors: 0},
		{name: "name too short", input: "AB, 5000, monthly, 8", wantName: "AB", wantAmount: "5000", wantFreq: "monthly", wantTarget: "8", wantErrors: 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got := parseThriftConcatInput(test.input, testMinKobo, testMaxKobo)
			if got.Name != test.wantName {
				t.Errorf("Name = %q, want %q", got.Name, test.wantName)
			}
			if got.Amount != test.wantAmount {
				t.Errorf("Amount = %q, want %q", got.Amount, test.wantAmount)
			}
			if got.Frequency != test.wantFreq {
				t.Errorf("Frequency = %q, want %q", got.Frequency, test.wantFreq)
			}
			if got.Target != test.wantTarget {
				t.Errorf("Target = %q, want %q", got.Target, test.wantTarget)
			}
			if len(got.Errors) != test.wantErrors {
				t.Errorf("Errors = %v, want %d errors", got.Errors, test.wantErrors)
			}
		})
	}
}

func TestParseInvoiceSingleItemConcat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantOk  bool
		wantName string
	}{
		{name: "three fields", input: "Website design, 1, 25000", wantOk: true, wantName: "Website design"},
		{name: "comma in name", input: "Website, design, 1, 25000", wantOk: true, wantName: "Website, design"},
		{name: "two fields only", input: "Website design, 1", wantOk: false},
		{name: "invalid qty", input: "Website design, 0, 25000", wantOk: false},
		{name: "invalid price", input: "Website design, 1, 0.50", wantOk: false},
		{name: "name too short", input: "W, 1, 25000", wantOk: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			name, _, _, ok := parseInvoiceSingleItemConcat(test.input, testMaxKobo)
			if ok != test.wantOk {
				t.Fatalf("ok = %v, want %v", ok, test.wantOk)
			}
			if ok && name != test.wantName {
				t.Errorf("name = %q, want %q", name, test.wantName)
			}
		})
	}
}

func TestParseInvoiceBulkItems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantErrs  int
	}{
		{name: "three valid items", input: "Website design, 1, 25000\nLogo design, 2, 15000\nHosting, 1, 5000", wantCount: 3, wantErrs: 0},
		{name: "item with error", input: "Website design, 1, 25000\nX, 0, 5000", wantCount: 2, wantErrs: 2},
		{name: "empty lines", input: "\nWebsite design, 1, 25000\n\n", wantCount: 1, wantErrs: 0},
		{name: "single item", input: "Website design, 1, 25000", wantCount: 1, wantErrs: 0},
		{name: "name only per line", input: "Website design\nLogo design", wantCount: 2, wantErrs: 0},
		{name: "eleven items", input: "Item, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000\nItem, 1, 25000", wantCount: 11, wantErrs: 0},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got := parseInvoiceBulkItems(test.input, testMaxKobo)
			if len(got) != test.wantCount {
				t.Fatalf("count = %d, want %d", len(got), test.wantCount)
			}
			errCount := 0
			for _, item := range got {
				errCount += len(item.Errors)
			}
			if errCount != test.wantErrs {
				t.Errorf("errors = %d, want %d", errCount, test.wantErrs)
			}
		})
	}
}
