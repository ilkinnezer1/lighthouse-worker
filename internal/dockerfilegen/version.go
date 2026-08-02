package dockerfilegen

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxManifestBytes = 1 << 20

const (
	defaultPHP    = "8.3"
	defaultNode   = "22"
	defaultPython = "3.12"
	defaultRuby   = "3.3"
)

var (
	reMajorMinor  = regexp.MustCompile(`([0-9]+)\.([0-9]+)`)
	reMajor       = regexp.MustCompile(`[0-9]+`)
	reGemfileRuby = regexp.MustCompile(`ruby\s+['"]v?([0-9]+\.[0-9]+)`)
)

// phpVersion resolves the PHP minor tag from composer.json's require.php
// constraint (the lower bound of e.g. ">=7.3", "^8.1"), or the default.
func phpVersion(srcDir, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	var m struct {
		Require map[string]string `json:"require"`
	}
	if readJSON(filepath.Join(srcDir, "composer.json"), &m) == nil {
		if v := majorMinor(m.Require["php"]); v != "" {
			return v
		}
	}
	return defaultPHP
}

// nodeVersion resolves the Node major tag from package.json engines.node, then
// .nvmrc, then the default.
func nodeVersion(srcDir, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	var m struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if readJSON(filepath.Join(srcDir, "package.json"), &m) == nil {
		if v := majorVersion(m.Engines.Node); v != "" {
			return v
		}
	}
	if b, err := readManifest(filepath.Join(srcDir, ".nvmrc")); err == nil {
		if v := majorVersion(string(b)); v != "" {
			return v
		}
	}
	return defaultNode
}

// pythonVersion resolves the Python minor tag from .python-version, then
// runtime.txt (heroku "python-3.11.4"), then the default.
func pythonVersion(srcDir, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	for _, name := range []string{".python-version", "runtime.txt"} {
		if b, err := readManifest(filepath.Join(srcDir, name)); err == nil {
			if v := majorMinor(string(b)); v != "" {
				return v
			}
		}
	}
	return defaultPython
}

// rubyVersion resolves the Ruby minor tag from .ruby-version, then a Gemfile
// `ruby "x.y"` line, then the default.
func rubyVersion(srcDir, override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	if b, err := readManifest(filepath.Join(srcDir, ".ruby-version")); err == nil {
		if v := majorMinor(string(b)); v != "" {
			return v
		}
	}
	if b, err := readManifest(filepath.Join(srcDir, "Gemfile")); err == nil {
		if m := reGemfileRuby.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return defaultRuby
}

func majorMinor(s string) string {
	m := reMajorMinor.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

func majorVersion(s string) string {
	return reMajor.FindString(s)
}

func readManifest(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxManifestBytes))
}

func readJSON(path string, v any) error {
	b, err := readManifest(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
