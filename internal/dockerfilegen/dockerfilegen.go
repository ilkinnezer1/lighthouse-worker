// Package dockerfilegen synthesizes a version-matched Dockerfile for a cloned
// repo that ships no Dockerfile or compose file, so it can be built through the
// standard Dockerfile path and target any legacy or current base-image version.
package dockerfilegen

import "fmt"

// DefaultPort is the internal port a generated image listens on (via $PORT) and
// that the run stage routes Traefik to when buildConfig sets no explicit port.
const DefaultPort = 8080

// Options carries the buildConfig overrides that steer generation. Each empty
// field falls back to the per-stack default; a set field wins.
type Options struct {
	BuildCommand   string
	StartCommand   string
	RuntimeVersion string
	Port           int
}

// Generate returns a Dockerfile for srcDir and the internal port its CMD binds.
// framework is the slug from framework.Detect(srcDir) ("" for none, which needs
// an explicit StartCommand or returns an error the caller writes to the log).
func Generate(srcDir, framework string, opts Options) (dockerfile string, port int, err error) {
	port = opts.Port
	if port == 0 {
		port = DefaultPort
	}
	switch framework {
	case "laravel":
		base := "php:" + phpVersion(srcDir, opts.RuntimeVersion) + "-cli"
		start := firstNonEmpty(opts.StartCommand, "php artisan serve --host 0.0.0.0 --port ${PORT}")
		return phpDockerfile(base, opts.BuildCommand, start, port), port, nil
	case "symfony":
		base := "php:" + phpVersion(srcDir, opts.RuntimeVersion) + "-cli"
		start := firstNonEmpty(opts.StartCommand, "php -S 0.0.0.0:${PORT} -t public")
		return phpDockerfile(base, opts.BuildCommand, start, port), port, nil
	case "node-prisma":
		base := "node:" + nodeVersion(srcDir, opts.RuntimeVersion) + "-alpine"
		start := firstNonEmpty(opts.StartCommand, "npm start")
		return nodeDockerfile(base, opts.BuildCommand, start, port), port, nil
	case "django":
		base := "python:" + pythonVersion(srcDir, opts.RuntimeVersion) + "-slim"
		start := firstNonEmpty(opts.StartCommand, "python manage.py runserver 0.0.0.0:${PORT}")
		return pythonDockerfile(base, opts.BuildCommand, start, port), port, nil
	case "rails":
		base := "ruby:" + rubyVersion(srcDir, opts.RuntimeVersion) + "-slim"
		start := firstNonEmpty(opts.StartCommand, "bundle exec rails server -b 0.0.0.0 -p ${PORT}")
		return rubyDockerfile(base, opts.BuildCommand, start, port), port, nil
	default:
		df, err := genericDockerfile(opts.BuildCommand, opts.StartCommand, port)
		return df, port, err
	}
}

// phpDockerfile serves Laravel/Symfony from php:<ver>-cli. Debian (not alpine) so
// docker-php-ext-install works on legacy PHP where musl breaks old extension
// builds; composer 2.2 is the LTS line that still runs on PHP 5.3.2+.
func phpDockerfile(base, buildCmd, startCmd string, port int) string {
	return fmt.Sprintf(`FROM %s
RUN apt-get update && apt-get install -y --no-install-recommends \
        git unzip libzip-dev libpq-dev libonig-dev \
    && docker-php-ext-install pdo pdo_mysql pdo_pgsql mbstring bcmath \
    && rm -rf /var/lib/apt/lists/*
COPY --from=composer:2.2 /usr/bin/composer /usr/bin/composer
WORKDIR /app
COPY . .
RUN composer install --no-interaction --prefer-dist --no-progress || true
%sENV PORT=%d
EXPOSE %d
CMD %s
`, base, runLine(buildCmd), port, port, startCmd)
}

func nodeDockerfile(base, buildCmd, startCmd string, port int) string {
	if buildCmd == "" {
		buildCmd = "npm run build --if-present"
	}
	return fmt.Sprintf(`FROM %s
WORKDIR /app
COPY . .
RUN if [ -f pnpm-lock.yaml ]; then corepack enable && pnpm install --frozen-lockfile; \
    elif [ -f yarn.lock ]; then yarn install --frozen-lockfile; \
    elif [ -f package-lock.json ]; then npm ci; \
    else npm install; fi
RUN npx prisma generate 2>/dev/null || true
RUN %s
ENV PORT=%d
EXPOSE %d
CMD %s
`, base, buildCmd, port, port, startCmd)
}

func pythonDockerfile(base, buildCmd, startCmd string, port int) string {
	if buildCmd == "" {
		buildCmd = "python manage.py collectstatic --noinput || true"
	}
	return fmt.Sprintf(`FROM %s
ENV PYTHONUNBUFFERED=1
WORKDIR /app
COPY . .
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi
RUN %s
ENV PORT=%d
EXPOSE %d
CMD %s
`, base, buildCmd, port, port, startCmd)
}

func rubyDockerfile(base, buildCmd, startCmd string, port int) string {
	return fmt.Sprintf(`FROM %s
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential git libpq-dev libsqlite3-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY . .
RUN bundle install
%sENV PORT=%d
EXPOSE %d
CMD %s
`, base, runLine(buildCmd), port, port, startCmd)
}

// genericDockerfile is the last resort when no stack is detected: it can only
// honor an explicit StartCommand, so it errors (with copy-pasteable guidance)
// when none is set rather than emit an unrunnable image.
func genericDockerfile(buildCmd, startCmd string, port int) (string, error) {
	if startCmd == "" {
		return "", fmt.Errorf("no Dockerfile or compose file, and no supported framework detected — " +
			"add a Dockerfile to the repo, or set a start command (and build command) in the project's build settings")
	}
	return fmt.Sprintf(`FROM debian:bookworm-slim
WORKDIR /app
COPY . .
%sENV PORT=%d
EXPOSE %d
CMD %s
`, runLine(buildCmd), port, port, startCmd), nil
}

func runLine(cmd string) string {
	if cmd == "" {
		return ""
	}
	return "RUN " + cmd + "\n"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
