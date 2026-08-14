package syncmapcopy

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	t.Parallel()

	testdata := analysistest.TestData()
	tests := []struct {
		name    string
		pattern string
	}{
		{name: "copy contexts and flow", pattern: "a"},
		{name: "all API methods", pattern: "methods"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysistest.Run(t, testdata, Analyzer, test.pattern)
		})
	}
}
