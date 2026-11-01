package nftset

import (
	"reflect"
	"testing"
)

func TestParseSetElements(t *testing.T) {
	output := `table inet fw4 {
	set example_v4 {
		type ipv4_addr
		elements = { 192.0.2.1,
			198.51.100.2 }
	}

	func TestParseSetReference(t *testing.T) {
		tests := []struct {
			input              string
			family, table, set string
			wantErr            bool
		}{
			{input: "blocked_v4", family: "inet", table: "fw4", set: "blocked_v4"},
			{input: "ip/filter/blocked_v4", family: "ip", table: "filter", set: "blocked_v4"},
			{input: "inet/fw4/", wantErr: true},
			{input: "inet/fw4/a/extra", wantErr: true},
			{input: "", wantErr: true},
		}

		for _, test := range tests {
			family, table, set, err := parseSetReference(test.input)
			if (err != nil) != test.wantErr {
				t.Errorf("parseSetReference(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
				continue
			}
			if err == nil && (family != test.family || table != test.table || set != test.set) {
				t.Errorf("parseSetReference(%q) = %s/%s/%s, want %s/%s/%s",
					test.input, family, table, set, test.family, test.table, test.set)
			}
		}
	}
}`
	got := parseSetElements(output)
	want := []string{"192.0.2.1", "198.51.100.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSetElements() = %v, want %v", got, want)
	}
}
