package dockerfilegen

import (
	"slices"
	"strings"
	"testing"
)

const webjobsComposerJSON = `{
  "require": {
    "php": "^8.3",
    "ext-bcmath": "*", "ext-curl": "*", "ext-fileinfo": "*", "ext-gd": "*",
    "ext-intl": "*", "ext-json": "*", "ext-libxml": "*", "ext-openssl": "*",
    "ext-simplexml": "*", "ext-xmlreader": "*", "ext-zip": "*",
    "laravel/framework": "^13.0",
    "laravel/horizon": "^5.0"
  },
  "require-dev": {"phpunit/phpunit": "^12.5"},
  "scripts": {
    "post-autoload-dump": ["@php artisan package:discover --ansi"],
    "post-install-cmd": ["@php artisan passport:keys --ansi"],
    "test": ["vendor/bin/phpunit"]
  }
}`

const webjobsComposerLock = `{
  "packages": [
    {"name": "laravel/framework", "require": {"php": "^8.3", "ext-ctype": "*", "ext-mbstring": "*", "ext-openssl": "*", "ext-tokenizer": "*"}},
    {"name": "laravel/horizon", "require": {"ext-json": "*", "ext-pcntl": "*", "ext-posix": "*"}},
    {"name": "lcobucci/jwt", "require": {"ext-openssl": "*", "ext-sodium": "*"}},
    {"name": "phpoffice/phpword", "require": {"ext-dom": "*", "ext-gd": "*", "ext-zip": "*"}}
  ],
  "packages-dev": [
    {"name": "phar-io/manifest", "require": {"ext-phar": "*", "ext-xmlwriter": "*"}}
  ],
  "platform": {"php": "^8.3", "ext-intl": "*"},
  "platform-dev": {}
}`

func webjobsFixture(t *testing.T) string {
	return writeFiles(t, map[string]string{
		"composer.json":                webjobsComposerJSON,
		"composer.lock":                webjobsComposerLock,
		"config/database.php":          `<?php return ['redis' => ['client' => env('REDIS_CLIENT', 'phpredis')]];`,
		"config/cache.php":             `<?php return ['default' => env('CACHE_DRIVER', 'file')];`,
		"app/Jobs/Core/CacheFlush.php": "<?php\nuse Illuminate\\Cache\\MemcachedStore;\n",
	})
}

func TestPHPExtensionsWebjobs(t *testing.T) {
	got := phpExtensions(webjobsFixture(t))
	want := []string{"bcmath", "gd", "intl", "memcached", "pcntl", "pdo_mysql", "pdo_pgsql", "redis", "sodium", "zip"}
	if !slices.Equal(got, want) {
		t.Fatalf("phpExtensions = %v, want %v", got, want)
	}
}

func TestPHPExtensionsBaseSetWithoutManifest(t *testing.T) {
	got := phpExtensions(t.TempDir())
	if !slices.Equal(got, phpBaseExtensions) {
		t.Fatalf("phpExtensions = %v, want %v", got, phpBaseExtensions)
	}
}

func TestPHPExtensionsLockOnlyRequirement(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"composer.json": `{"require":{"php":">=7.3","laravel/horizon":"^3.0"}}`,
		"composer.lock": `{"packages":[{"name":"laravel/horizon","require":{"ext-pcntl":"*","ext-posix":"*"}}]}`,
	})
	got := phpExtensions(dir)
	if !slices.Contains(got, "pcntl") {
		t.Fatalf("phpExtensions = %v, want pcntl from the lock", got)
	}
	if slices.Contains(got, "posix") {
		t.Fatalf("phpExtensions = %v, built-in posix must be skipped", got)
	}
}

func TestPHPExtensionsPredisSkipsPhpredis(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"composer.json":       `{"require":{"predis/predis":"^2.0"}}`,
		"config/database.php": `'client' => env('REDIS_CLIENT', 'phpredis')`,
	})
	if got := phpExtensions(dir); slices.Contains(got, "redis") {
		t.Fatalf("phpExtensions = %v, predis needs no redis extension", got)
	}
}

func TestPHPExtensionsMemcachedFromConfigDefault(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"config/cache.php": `'default' => env('CACHE_DRIVER', 'memcached'),`,
	})
	if got := phpExtensions(dir); !slices.Contains(got, "memcached") {
		t.Fatalf("phpExtensions = %v, want memcached", got)
	}
}

func TestExtFromRequirement(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ext-pdo_mysql", "pdo_mysql", true},
		{"EXT-GD", "gd", true},
		{"ext-zend-opcache", "opcache", true},
		{"ext-foo; rm -rf /", "", false},
		{"php", "", false},
		{"laravel/framework", "", false},
	}
	for _, c := range cases {
		got, ok := extFromRequirement(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("extFromRequirement(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestComposerImageTag(t *testing.T) {
	cases := map[string]string{
		"8.3": "2", "7.4": "2", "7.2": "2", "7.1": "2.2", "5.6": "2.2", "latest": "2",
	}
	for ver, want := range cases {
		if got := composerImageTag(ver); got != want {
			t.Fatalf("composerImageTag(%q) = %q, want %q", ver, got, want)
		}
	}
}

func TestGenerateLaravelWebjobsDockerfile(t *testing.T) {
	df, _, err := Generate(webjobsFixture(t), "laravel", Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := `FROM php:8.3-cli
ADD https://github.com/mlocati/docker-php-extension-installer/releases/download/2.12.3/install-php-extensions /usr/local/bin/
RUN chmod +x /usr/local/bin/install-php-extensions \
    && apt-get update && apt-get install -y --no-install-recommends git unzip \
    && rm -rf /var/lib/apt/lists/* \
    && install-php-extensions bcmath gd intl memcached pcntl pdo_mysql pdo_pgsql redis sodium zip
COPY --from=composer:2 /usr/bin/composer /usr/bin/composer
ENV COMPOSER_ALLOW_SUPERUSER=1
WORKDIR /app
COPY . .
RUN mkdir -p bootstrap/cache storage/framework/cache/data storage/framework/sessions storage/framework/views storage/logs
RUN composer install --no-interaction --prefer-dist --no-progress --no-scripts
RUN composer run-script post-autoload-dump --no-interaction \
    || echo "warning: composer post-autoload-dump scripts failed at build time (no runtime env or services); continuing" >&2
RUN composer run-script post-install-cmd --no-interaction \
    || echo "warning: composer post-install-cmd scripts failed at build time (no runtime env or services); continuing" >&2
ENV PORT=8080
EXPOSE 8080
CMD php artisan serve --host 0.0.0.0 --port ${PORT}
`
	if df != want {
		t.Fatalf("dockerfile mismatch:\n--- got\n%s\n--- want\n%s", df, want)
	}
}

func TestGenerateLegacyPHPKeepsComposerLTS(t *testing.T) {
	dir := writeFiles(t, map[string]string{"composer.json": `{"require":{"php":">=7.1"}}`})
	df, _, err := Generate(dir, "laravel", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FROM php:7.1-cli", "COPY --from=composer:2.2 ", "install-php-extensions bcmath pdo_mysql pdo_pgsql\n"} {
		if !strings.Contains(df, want) {
			t.Fatalf("dockerfile missing %q:\n%s", want, df)
		}
	}
	if strings.Contains(df, "|| true") || strings.Contains(df, "run-script") {
		t.Fatalf("composer failures must not be swallowed and undeclared hooks not run:\n%s", df)
	}
}
