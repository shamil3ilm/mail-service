package api

import "testing"

func TestRewriteQuery(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"receipt paid", "receipt paid"},
		// Operators are rewritten to column names + auto-quoted email vals.
		{"from:alice@x.com", `from_addr:"alice@x.com"`},
		{"to:orders", "to_addrs:orders"},
		{`subject:"weekly report"`, `subject:"weekly report"`},
		// Bare email tokens are quoted so tokeniser doesn't split them.
		{"alice@x.com", `"alice@x.com"`},
		// Unknown operators are passed through untouched.
		{"whatever:hi", "whatever:hi"},
		// Mixed usage.
		{"from:alice payment", `from_addr:alice payment`},
	}
	for _, tc := range cases {
		if got := rewriteQuery(tc.in); got != tc.want {
			t.Errorf("rewriteQuery(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
