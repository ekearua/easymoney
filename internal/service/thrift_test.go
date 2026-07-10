package service

import "testing"

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
