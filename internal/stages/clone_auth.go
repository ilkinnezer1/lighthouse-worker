package stages

import (
	"net/url"
	"strings"
)

// authedRemote injects an OAuth token into an HTTPS github.com remote as the
// userinfo `x-access-token:<token>`, returning both the real remote and a
// log-safe copy with the token masked. Any non-github/non-https/empty-token
// case is returned unchanged so public clones keep working. The masked form is
// the ONLY representation that may ever reach a build log — the token must not.
func authedRemote(repoURL, token string) (remote, logSafe string) {
	u, err := url.Parse(repoURL)
	if err != nil || token == "" || u.Scheme != "https" || u.Hostname() != "github.com" {
		return repoURL, repoURL
	}
	u.User = url.UserPassword("x-access-token", token)
	remote = u.String()
	logSafe = strings.Replace(remote, u.User.String(), "x-access-token:***", 1)
	return remote, logSafe
}
