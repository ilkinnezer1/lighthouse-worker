// Package framework detects an app's framework from its checked-out source tree.
package framework

import (
	"bytes"
	"os"
	"path/filepath"
)

// maxManifestBytes caps how much of a manifest we read for signature matching, so
// a hostile or oversized file can't blow up the deploy.
const maxManifestBytes = 1 << 20

// Detect inspects the cloned source directory and returns the framework slug, or
// "" when none match. It is file-based and defensive: missing, unreadable, or
// oversized files are treated as absent and never error.
func Detect(srcDir string) string {
	switch {
	case exists(srcDir, "artisan") && manifestHas(srcDir, "composer.json", "laravel/framework"):
		return "laravel"
	case exists(srcDir, "bin/console") && manifestHas(srcDir, "composer.json", "symfony/framework-bundle"):
		return "symfony"
	case exists(srcDir, "package.json") &&
		(exists(srcDir, "prisma/schema.prisma") || manifestHas(srcDir, "package.json", `"prisma"`)):
		return "node-prisma"
	case (exists(srcDir, "bin/rails") || exists(srcDir, "config/application.rb")) &&
		manifestHas(srcDir, "Gemfile", "rails"):
		return "rails"
	case exists(srcDir, "manage.py"):
		return "django"
	default:
		return ""
	}
}

func exists(srcDir, rel string) bool {
	_, err := os.Stat(filepath.Join(srcDir, rel))
	return err == nil
}

func manifestHas(srcDir, rel, needle string) bool {
	f, err := os.Open(filepath.Join(srcDir, rel))
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, maxManifestBytes)
	n, _ := f.Read(buf)
	return bytes.Contains(buf[:n], []byte(needle))
}
