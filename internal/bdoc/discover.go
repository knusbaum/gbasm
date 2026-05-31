package bdoc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoverPackages walks each entry in bosonpath (colon-separated) and finds
// directories containing at least one .bos or .bs file. Each such directory
// is one package, identified by its path relative to its BOSONPATH entry.
// The returned slice is sorted by import path. If the same import path is
// found in more than one BOSONPATH entry, the earlier entry wins (mirroring
// the build system's resolve_pkg behaviour).
func DiscoverPackages(bosonpath string) ([]*PackageScan, error) {
	seen := make(map[string]bool)
	var packages []*PackageScan

	for _, root := range strings.Split(bosonpath, ":") {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bdoc: bad BOSONPATH entry %q: %v\n", root, err)
			continue
		}
		if _, err := os.Stat(absRoot); err != nil {
			// Missing path is not fatal; some entries may not exist in every project.
			continue
		}

		err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if !d.IsDir() {
				return nil
			}
			// Skip per-package work directories produced by boson.mmk. They
			// hold intermediate .bs files generated from .bos sources and
			// would otherwise show up as duplicate "packages" in the index.
			if strings.HasSuffix(d.Name(), ".work") {
				return filepath.SkipDir
			}
			if !dirHasBosonSources(path) {
				return nil
			}
			rel, err := filepath.Rel(absRoot, path)
			if err != nil {
				return nil
			}
			if rel == "." {
				rel = filepath.Base(path)
			}
			importPath := filepath.ToSlash(rel)
			if strings.HasPrefix(filepath.Base(importPath), "_") {
				return nil
			}
			if seen[importPath] {
				return nil
			}
			seen[importPath] = true
			ps, err := ScanPackage(path, importPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "bdoc: scanning %s: %v\n", path, err)
				return nil
			}
			packages = append(packages, ps)
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "bdoc: walking %s: %v\n", absRoot, err)
		}
	}

	sort.Slice(packages, func(i, j int) bool {
		return packages[i].ImportPath < packages[j].ImportPath
	})
	return packages, nil
}

func dirHasBosonSources(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".bos") || strings.HasSuffix(n, ".bs") {
			return true
		}
	}
	return false
}
