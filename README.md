# go-sync-map-copy-analyzer

A focused [`go/analysis`](https://pkg.go.dev/golang.org/x/tools/go/analysis) analyzer for the documented `sync.Map` rule:

> A Map must not be copied after first use.

```go
var valid sync.Map
validCopy := valid // allowed: valid is definitely unused

var invalid sync.Map
invalid.Store("key", "value")
invalidCopy := invalid // syncmapcopy: sync.Map is copied after first use
```

The analyzer tracks each local `sync.Map` as definitely unused, definitely used, or unknown. Every current `sync.Map` API method marks its receiver as used. It detects later value copies in assignments, declarations, returns, calls, and composite values. It also follows direct pointer aliases and summarizes local functions that receive `*sync.Map`.

Unknown state is conservative: the analyzer reports only definite copy-after-use violations. For example, passing `&m` to an opaque function makes `m` unknown and suppresses a later diagnostic.

## Why this is separate from `copylocks`

`copylocks` detects types that must not be copied at all. That type-only check cannot enforce the `sync.Map` contract correctly because a zero, unused `sync.Map` may be copied. This analyzer adds value-specific control-flow and local interprocedural state tracking.

## Run in another project

```sh
go run github.com/gmcabrita/go-sync-map-copy-analyzer/cmd/syncmapcopy@latest ./...
```

Or install it:

```sh
go install github.com/gmcabrita/go-sync-map-copy-analyzer/cmd/syncmapcopy@latest
syncmapcopy ./...
```

Arguments are standard Go package patterns.

## Embed the analyzer

```go
package main

import (
	syncmapcopy "github.com/gmcabrita/go-sync-map-copy-analyzer"
	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(syncmapcopy.Analyzer)
}
```

## Current scope

The analysis prioritizes definite violations. It does not infer definite use through opaque or cross-package pointer calls, escaped pointers, function values, pointers to aggregates that contain a map, dynamic array indexes, or arrays longer than 256 elements. Package-global state, range-element copies, and violations that become visible only on a later loop iteration are also outside the current model. These choices can cause false negatives, not intentional false positives.

## Development

```sh
mise run check
```
