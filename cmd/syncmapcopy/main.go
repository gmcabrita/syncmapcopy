// Command syncmapcopy runs the sync.Map copy-after-use analyzer.
package main

import (
	syncmapcopy "github.com/gmcabrita/syncmapcopy"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(syncmapcopy.Analyzer)
}
