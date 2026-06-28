// gen regenerates internal/pricing/data.json from the live pi.dev/models catalog.
// It is the same data source used at runtime for cache refreshes.
//
// Usage (from repo root):
//
//	go run ./internal/pricing/gen
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mintoleda/talos/internal/pricing"
)

func main() {
	data, err := pricing.FetchLive(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch: %v\n", err)
		os.Exit(1)
	}
	b, err := pricing.MarshalRaw(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	dest := "internal/pricing/data.json"
	if err := os.WriteFile(dest, b, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", dest, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d models to %s\n", len(data), dest)
}
