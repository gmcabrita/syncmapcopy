// Package syncmapcopy provides an analyzer that reports copies of sync.Map
// values after their first use.
//
// Unlike copylocks, this analyzer uses flow-sensitive state. A zero sync.Map
// may be copied, but a value becomes non-copyable after any sync.Map API method
// is called on it.
package syncmapcopy
