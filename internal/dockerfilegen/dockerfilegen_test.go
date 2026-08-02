package dockerfilegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPHPVersion(t *testing.T) {
	cases := []struct {
		name       string
		require    string
		override   string
		wantSuffix string
	}{
		{"gte", `{"require":{"php":">=7.3"}}`, "", "7.3"},
		{"caret", `{"require":{"php":"^8.1"}}`, "", "8.1"},
		{"tilde patch", `{"require":{"php":"~7.2.5"}}`, "", "7.2"},
		{"wildcard", `{"require":{"php":"8.0.*"}}`, "", "8.0"},
		{"range", `{"require":{"php":">=7.4 <8.1"}}`, "", "7.4"},
		{"no php key falls to default", `{"require":{}}`, "", defaultPHP},
		{"override wins", `{"require":{"php":">=7.3"}}`, "5.6", "5.6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{"composer.json": c.require})
			if got := phpVersion(dir, c.override); got != c.wantSuffix {
				t.Fatalf("phpVersion = %q, want %q", got, c.wantSuffix)
			}
		})
	}

	t.Run("missing manifest defaults", func(t *testing.T) {
		if got := phpVersion(t.TempDir(), ""); got != defaultPHP {
			t.Fatalf("phpVersion = %q, want %q", got, defaultPHP)
		}
	})
}

func TestNodeVersion(t *testing.T) {
	t.Run("engines wins over nvmrc", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			"package.json": `{"engines":{"node":">=18.0.0"}}`,
			".nvmrc":       "20",
		})
		if got := nodeVersion(dir, ""); got != "18" {
			t.Fatalf("nodeVersion = %q, want 18", got)
		}
	})
	t.Run("nvmrc fallback", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{".nvmrc": "v20.11.0"})
		if got := nodeVersion(dir, ""); got != "20" {
			t.Fatalf("nodeVersion = %q, want 20", got)
		}
	})
	t.Run("default", func(t *testing.T) {
		if got := nodeVersion(t.TempDir(), ""); got != defaultNode {
			t.Fatalf("nodeVersion = %q, want %q", got, defaultNode)
		}
	})
	t.Run("override wins", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"package.json": `{"engines":{"node":"18"}}`})
		if got := nodeVersion(dir, "16"); got != "16" {
			t.Fatalf("nodeVersion = %q, want 16", got)
		}
	})
}

func TestPythonVersion(t *testing.T) {
	t.Run("python-version wins", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			".python-version": "3.11.4",
			"runtime.txt":     "python-3.9.1",
		})
		if got := pythonVersion(dir, ""); got != "3.11" {
			t.Fatalf("pythonVersion = %q, want 3.11", got)
		}
	})
	t.Run("runtime.txt fallback", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"runtime.txt": "python-3.9.1"})
		if got := pythonVersion(dir, ""); got != "3.9" {
			t.Fatalf("pythonVersion = %q, want 3.9", got)
		}
	})
	t.Run("default", func(t *testing.T) {
		if got := pythonVersion(t.TempDir(), ""); got != defaultPython {
			t.Fatalf("pythonVersion = %q, want %q", got, defaultPython)
		}
	})
}

func TestRubyVersion(t *testing.T) {
	t.Run("ruby-version wins", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{
			".ruby-version": "3.2.2",
			"Gemfile":       `ruby "3.0.0"`,
		})
		if got := rubyVersion(dir, ""); got != "3.2" {
			t.Fatalf("rubyVersion = %q, want 3.2", got)
		}
	})
	t.Run("gemfile fallback", func(t *testing.T) {
		dir := writeFiles(t, map[string]string{"Gemfile": "source 'https://rubygems.org'\nruby '3.1.4'\n"})
		if got := rubyVersion(dir, ""); got != "3.1" {
			t.Fatalf("rubyVersion = %q, want 3.1", got)
		}
	})
	t.Run("default", func(t *testing.T) {
		if got := rubyVersion(t.TempDir(), ""); got != defaultRuby {
			t.Fatalf("rubyVersion = %q, want %q", got, defaultRuby)
		}
	})
}

func TestGenerateTemplateSelection(t *testing.T) {
	cases := []struct {
		framework string
		files     map[string]string
		wantBase  string
		wantCMD   string
	}{
		{"laravel", map[string]string{"composer.json": `{"require":{"php":"^8.1"}}`}, "FROM php:8.1-cli", "CMD php artisan serve"},
		{"symfony", map[string]string{"composer.json": `{"require":{"php":">=7.4"}}`}, "FROM php:7.4-cli", "CMD php -S 0.0.0.0:${PORT} -t public"},
		{"node-prisma", map[string]string{"package.json": `{"engines":{"node":"20"}}`}, "FROM node:20-alpine", "CMD npm start"},
		{"django", map[string]string{".python-version": "3.11"}, "FROM python:3.11-slim", "CMD python manage.py runserver 0.0.0.0:${PORT}"},
		{"rails", map[string]string{".ruby-version": "3.2"}, "FROM ruby:3.2-slim", "CMD bundle exec rails server -b 0.0.0.0 -p ${PORT}"},
	}
	for _, c := range cases {
		t.Run(c.framework, func(t *testing.T) {
			dir := writeFiles(t, c.files)
			df, port, err := Generate(dir, c.framework, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if port != DefaultPort {
				t.Fatalf("port = %d, want %d", port, DefaultPort)
			}
			if !strings.Contains(df, c.wantBase) {
				t.Fatalf("dockerfile missing base %q:\n%s", c.wantBase, df)
			}
			if !strings.Contains(df, c.wantCMD) {
				t.Fatalf("dockerfile missing cmd %q:\n%s", c.wantCMD, df)
			}
		})
	}
}

func TestGenerateOverridePrecedence(t *testing.T) {
	dir := writeFiles(t, map[string]string{"composer.json": `{"require":{"php":"^8.1"}}`})
	df, port, err := Generate(dir, "laravel", Options{
		RuntimeVersion: "7.2",
		BuildCommand:   "composer run-script build",
		StartCommand:   "php custom-serve",
		Port:           9000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if port != 9000 {
		t.Fatalf("port = %d, want 9000", port)
	}
	for _, want := range []string{"FROM php:7.2-cli", "RUN composer run-script build", "CMD php custom-serve", "ENV PORT=9000"} {
		if !strings.Contains(df, want) {
			t.Fatalf("dockerfile missing %q:\n%s", want, df)
		}
	}
	if strings.Contains(df, "php artisan serve") {
		t.Fatalf("start command override ignored:\n%s", df)
	}
}

func TestGenerateGenericFallback(t *testing.T) {
	t.Run("no start command errors", func(t *testing.T) {
		if _, _, err := Generate(t.TempDir(), "", Options{}); err == nil {
			t.Fatal("expected error when no framework and no start command")
		}
	})
	t.Run("start command honored", func(t *testing.T) {
		df, _, err := Generate(t.TempDir(), "", Options{StartCommand: "./server", BuildCommand: "make"})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"FROM debian:bookworm-slim", "RUN make", "CMD ./server"} {
			if !strings.Contains(df, want) {
				t.Fatalf("dockerfile missing %q:\n%s", want, df)
			}
		}
	})
}
