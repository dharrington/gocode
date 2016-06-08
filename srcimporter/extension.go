package srcimporter

import "github.com/mdempsky/gocode/gbimporter"

// These functions exist to allow environment-specific customization.

func extendGopath(ctx *gbimporter.PackedContext, filename string, paths *[]string) {}
func extendLookupPaths(p *pkgCache, pkgDir, pkgPath string, paths *[]string)       {}
func changePackageName(srcFilePath string, pkgName string) string                  {}
