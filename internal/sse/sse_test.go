package sse

import "testing"

func TestExtractUsageTokens(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{`data: {"choices":[{"delta":{"content":"hi"}}]}`, 0},
		{`data: {"choices":[],"usage":{"completion_tokens":42}}`, 42},
		{`data: [DONE]`, 0},
		{``, 0},
		{`not an sse line`, 0},
		{`data: not json`, 0},
	}
	for _, c := range cases {
		if got := ExtractUsageTokens(c.line); got != c.want {
			t.Errorf("ExtractUsageTokens(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}
