package dockerfilegen

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	phpExtInstallerVersion = "2.12.3"
	phpExtInstallerURL     = "https://github.com/mlocati/docker-php-extension-installer/releases/download/" +
		phpExtInstallerVersion + "/install-php-extensions"
	maxScannedSourceFiles = 10000
)

var phpBaseExtensions = []string{"bcmath", "pdo_mysql", "pdo_pgsql"}

// compiled into every official php:*-cli image from 5.6 onward; sodium, random
// and opcache vary by version so install-php-extensions decides those.
var phpBuiltinExtensions = map[string]bool{
	"core": true, "ctype": true, "curl": true, "date": true, "dom": true, "fileinfo": true,
	"filter": true, "hash": true, "iconv": true, "json": true, "libxml": true, "mbstring": true,
	"mysqlnd": true, "openssl": true, "pcre": true, "pdo": true, "pdo_sqlite": true, "phar": true,
	"posix": true, "readline": true, "reflection": true, "session": true, "simplexml": true,
	"spl": true, "sqlite3": true, "standard": true, "tokenizer": true, "xml": true,
	"xmlreader": true, "xmlwriter": true, "zlib": true,
}

var (
	reExtName          = regexp.MustCompile(`^[a-z0-9_]+$`)
	reMemcachedUsage   = regexp.MustCompile(`MemcachedStore|\bnew\s+\\?Memcached\b|\\Memcached::`)
	reMemcachedDriver  = regexp.MustCompile(`(?m)^\s*(CACHE_DRIVER|CACHE_STORE|SESSION_DRIVER)\s*=\s*"?memcached`)
	reMemcachedDefault = regexp.MustCompile(`env\(\s*['"](CACHE_DRIVER|CACHE_STORE|SESSION_DRIVER)['"]\s*,\s*['"]memcached['"]`)
	reRedisDriver      = regexp.MustCompile(`(?m)^\s*(CACHE_DRIVER|CACHE_STORE|SESSION_DRIVER|QUEUE_CONNECTION|QUEUE_DRIVER)\s*=\s*"?redis`)
	rePhpredisDefaults = regexp.MustCompile(`phpredis`)
)

type composerManifest struct {
	Require    map[string]string `json:"require"`
	RequireDev map[string]string `json:"require-dev"`
	Scripts    map[string]any    `json:"scripts"`
}

type composerLock struct {
	Packages    []composerPackage `json:"packages"`
	PackagesDev []composerPackage `json:"packages-dev"`
	Platform    map[string]string `json:"platform"`
	PlatformDev map[string]string `json:"platform-dev"`
}

type composerPackage struct {
	Name    string            `json:"name"`
	Require map[string]string `json:"require"`
}

// phpExtensions lists the extensions a generated PHP image installs: the base
// set, every ext-* composer will enforce (root and locked packages, dev
// included since install runs with dev), and redis/memcached when the app is
// wired to them at runtime without declaring the extension.
func phpExtensions(srcDir string) []string {
	var manifest composerManifest
	_ = readJSON(filepath.Join(srcDir, "composer.json"), &manifest)
	var lock composerLock
	_ = readJSON(filepath.Join(srcDir, "composer.lock"), &lock)

	set := map[string]bool{}
	add := func(reqs map[string]string) {
		for name := range reqs {
			if ext, ok := extFromRequirement(name); ok {
				set[ext] = true
			}
		}
	}
	add(manifest.Require)
	add(manifest.RequireDev)
	add(lock.Platform)
	add(lock.PlatformDev)
	packages := map[string]bool{}
	for _, p := range slices.Concat(lock.Packages, lock.PackagesDev) {
		packages[strings.ToLower(p.Name)] = true
		add(p.Require)
	}
	for name := range manifest.Require {
		packages[strings.ToLower(name)] = true
	}
	for _, ext := range phpBaseExtensions {
		set[ext] = true
	}
	if usesPhpredis(srcDir, packages) {
		set["redis"] = true
	}
	if usesMemcached(srcDir) {
		set["memcached"] = true
	}

	exts := make([]string, 0, len(set))
	for ext := range set {
		if !phpBuiltinExtensions[ext] {
			exts = append(exts, ext)
		}
	}
	slices.Sort(exts)
	return exts
}

func extFromRequirement(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	ext, ok := strings.CutPrefix(name, "ext-")
	if !ok {
		return "", false
	}
	if ext == "zend-opcache" || ext == "zend_opcache" {
		ext = "opcache"
	}
	if !reExtName.MatchString(ext) {
		return "", false
	}
	return ext, true
}

func usesPhpredis(srcDir string, packages map[string]bool) bool {
	if packages["predis/predis"] {
		return false
	}
	if b, err := readManifest(filepath.Join(srcDir, ".env.example")); err == nil && reRedisDriver.Match(b) {
		return true
	}
	if packages["laravel/horizon"] {
		return true
	}
	b, err := readManifest(filepath.Join(srcDir, "config", "database.php"))
	return err == nil && rePhpredisDefaults.Match(b)
}

func usesMemcached(srcDir string) bool {
	if b, err := readManifest(filepath.Join(srcDir, ".env.example")); err == nil && reMemcachedDriver.Match(b) {
		return true
	}
	for _, name := range []string{"cache.php", "session.php"} {
		if b, err := readManifest(filepath.Join(srcDir, "config", name)); err == nil && reMemcachedDefault.Match(b) {
			return true
		}
	}
	found := false
	scanned := 0
	_ = filepath.WalkDir(filepath.Join(srcDir, "app"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || found || scanned >= maxScannedSourceFiles {
			return fs.SkipAll
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".php") {
			return nil
		}
		scanned++
		if b, err := readManifest(path); err == nil && reMemcachedUsage.Match(b) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// composerImageTag keeps the 2.2 LTS line only for PHP older than 7.2.5, the
// floor of current Composer 2.
func composerImageTag(phpVer string) string {
	m := reMajorMinor.FindStringSubmatch(phpVer)
	if m == nil {
		return "2"
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 7 || (major == 7 && minor < 2) {
		return "2.2"
	}
	return "2"
}

func composerScriptHooks(srcDir string) []string {
	var manifest composerManifest
	if readJSON(filepath.Join(srcDir, "composer.json"), &manifest) != nil {
		return nil
	}
	var hooks []string
	for _, hook := range []string{"post-autoload-dump", "post-install-cmd"} {
		if _, ok := manifest.Scripts[hook]; ok {
			hooks = append(hooks, hook)
		}
	}
	return hooks
}
