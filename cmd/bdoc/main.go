// bdoc is the Boson documentation server. It walks BOSONPATH for packages
// and serves HTML documentation generated from their source comments.
//
// Usage:
//
//	bdoc [-addr :8686] [-path <bosonpath>]
//
// Defaults: -addr :8686, -path $BOSONPATH (or "." if unset).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

var (
	addr = flag.String("addr", ":8686", "HTTP listen address")
	path = flag.String("path", "", "Colon-separated package search path (defaults to $BOSONPATH, then '.')")
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

	fmt.Fprintf(os.Stderr, "bdoc: discovering packages in %s\n", bosonpath)
	packages, err := discoverPackages(bosonpath)
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	fmt.Fprintf(os.Stderr, "bdoc: found %d package(s)\n", len(packages))
	for _, p := range packages {
		fmt.Fprintf(os.Stderr, "  %s -> %s (%d decls)\n", p.ImportPath, p.Dir, len(p.Decls))
	}

	state := newDocState(packages)
	http.HandleFunc("/", state.serveIndex)
	http.HandleFunc("/pkg/", state.servePkg)
	http.HandleFunc("/styles.css", state.serveCSS)

	fmt.Fprintf(os.Stderr, "bdoc: listening on %s\n", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
