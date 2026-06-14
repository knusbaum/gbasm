// Package tourcontent embeds the Boson tour lesson tree so that btourd ships
// its lessons in the binary. The content is plain files under section
// directories; see internal/tour for the loader and lesson format.
//
// Each section directory must be listed in the //go:embed directive below.
package tourcontent

import "embed"

//go:embed 01-basics 02-data 03-pointers 04-ownership
var content embed.FS

// FS returns the embedded lesson tree, rooted at the section directories
// (e.g. "01-basics/01-hello/lesson.md").
func FS() embed.FS { return content }
