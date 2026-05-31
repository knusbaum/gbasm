// bdoc is the Boson documentation server. It walks BOSONPATH for packages
// and serves HTML documentation generated from their source comments.
//
// Usage:
//
//	bdoc [-addr :8686] [-path <bosonpath>] [-base /docs]
//
// Defaults: -addr :8686, -path $BOSONPATH (or "." if unset).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/knusbaum/gbasm/internal/bdoc"
)

var (
	addr = flag.String("addr", ":8686", "HTTP listen address")
	path = flag.String("path", "", "Colon-separated package search path (defaults to $BOSONPATH, then '.')")
	base = flag.String("base", "", "Base URL path when serving below a subdirectory")
)

func main() {
	flag.Parse()

	bosonpath := *path
	if bosonpath == "" {
		bosonpath = os.Getenv("BOSONPATH")
	}
	if bosonpath == "" {
		bosonpath = "."
	}

	// One-shot discovery at startup is purely for the listing logged
	// to stderr — every request re-discovers (see docState.snapshot)
	// so .bos / .bs edits show up on the next browser refresh
	// without restarting the server.
	fmt.Fprintf(os.Stderr, "bdoc: discovering packages in %s\n", bosonpath)
	packages, err := bdoc.DiscoverPackages(bosonpath)
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	fmt.Fprintf(os.Stderr, "bdoc: found %d package(s)\n", len(packages))
	for _, p := range packages {
		fmt.Fprintf(os.Stderr, "  %s -> %s (%d decls)\n", p.ImportPath, p.Dir, len(p.Decls))
	}

	fmt.Fprintf(os.Stderr, "bdoc: listening on %s\n", *addr)
	if err := http.ListenAndServe(*addr, bdoc.Handler(bdoc.Options{BosonPath: bosonpath, BasePath: *base})); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
