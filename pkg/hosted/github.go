package hosted

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

type github struct {
	cfg    Config
	client *http.Client
	key    *rsa.PrivateKey
}
type githubRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

func newGitHub(c Config) (*github, error) {
	g := &github{cfg: c, client: &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if c.Development && c.PrivateKeyFile == "" {
		return g, nil
	}
	b, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("invalid GitHub App private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(p.Bytes); err == nil {
		g.key = key
	} else {
		key, err := x509.ParsePKCS8PrivateKey(p.Bytes)
		if err != nil {
			return nil, err
		}
		var ok bool
		g.key, ok = key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("GitHub App needs an RSA key")
		}
	}
	return g, nil
}
func (g *github) jwt() (string, error) {
	if g.key == nil {
		return "", fmt.Errorf("GitHub App is not configured")
	}
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	b, _ := json.Marshal(map[string]any{"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(9 * time.Minute).Unix(), "iss": g.cfg.AppID})
	body := head + "." + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, h[:])
	return body + "." + base64.RawURLEncoding.EncodeToString(sig), err
}
func (g *github) request(ctx context.Context, method, path, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.cfg.GitHubAPI+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ownership-atlas")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub returned %d for %s", resp.StatusCode, path)
	}
	if output == nil {
		return nil
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(output); err != nil {
		return fmt.Errorf("invalid GitHub response")
	}
	return nil
}
func (g *github) identity(ctx context.Context, token string) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.request(ctx, "GET", "/user", token, nil, &u); err != nil {
		return "", err
	}
	if !loginPattern.MatchString(u.Login) {
		return "", fmt.Errorf("invalid GitHub identity")
	}
	// An installation token can see private memberships independently of which
	// repositories the signed-in member can access.
	app, err := g.jwt()
	if err != nil {
		return "", err
	}
	var ins struct {
		ID int64 `json:"id"`
	}
	if err = g.request(ctx, "GET", "/orgs/"+g.cfg.Organization+"/installation", app, nil, &ins); err != nil {
		return "", fmt.Errorf("install the GitHub App on the allowed organization")
	}
	install, err := g.installToken(ctx, ins.ID, nil)
	if err != nil {
		return "", err
	}
	var membership struct {
		State string `json:"state"`
	}
	if err = g.request(ctx, "GET", "/orgs/"+g.cfg.Organization+"/memberships/"+u.Login, install, nil, &membership); err != nil || membership.State != "active" {
		return "", fmt.Errorf("an active %s organization membership is required", g.cfg.Organization)
	}
	if g.cfg.EmailDomain != "" {
		var emails []struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
		}
		if err = g.request(ctx, "GET", "/user/emails", token, nil, &emails); err != nil {
			return "", fmt.Errorf("allow the app to read verified email addresses")
		}
		ok := false
		for _, e := range emails {
			_, domain, found := strings.Cut(e.Email, "@")
			if found && e.Verified && strings.EqualFold(domain, g.cfg.EmailDomain) {
				ok = true
			}
		}
		if !ok {
			return "", fmt.Errorf("a verified @%s email on your GitHub account is required", g.cfg.EmailDomain)
		}
	}
	return u.Login, nil
}
func (g *github) installToken(ctx context.Context, id int64, repos []int64) (string, error) {
	jwt, err := g.jwt()
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	input := map[string]any{}
	if len(repos) > 0 {
		input["repository_ids"] = repos
	}
	err = g.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id), jwt, input, &out)
	return out.Token, err
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func normalizeRepo(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "https://github.com/") {
		u, err := url.Parse(raw)
		if err != nil || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("use a GitHub repository URL or owner/name")
		}
		raw = strings.TrimPrefix(u.Path, "/")
	}
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "/"), ".git")
	if !repoPattern.MatchString(raw) || strings.HasSuffix(raw, "/.") || strings.HasSuffix(raw, "/..") {
		return "", fmt.Errorf("use a GitHub repository URL or owner/name")
	}
	return strings.ToLower(raw), nil
}
func (g *github) repository(ctx context.Context, name string) (githubRepo, string, error) {
	var r githubRepo
	var token string
	if g.key != nil {
		jwt, err := g.jwt()
		if err != nil {
			return r, "", err
		}
		var i struct {
			ID int64 `json:"id"`
		}
		if g.request(ctx, "GET", "/repos/"+name+"/installation", jwt, nil, &i) == nil {
			token, err = g.installToken(ctx, i.ID, nil)
			if err != nil {
				return r, "", err
			}
		}
	}
	if err := g.request(ctx, "GET", "/repos/"+name, token, nil, &r); err != nil {
		return r, "", fmt.Errorf("repository unavailable: check its name and the GitHub App installation access")
	}
	if r.Private && token == "" {
		return r, "", fmt.Errorf("install the GitHub App on this private repository")
	}
	if _, err := normalizeRepo(r.FullName); err != nil || r.ID <= 0 || r.DefaultBranch == "" {
		return r, "", fmt.Errorf("GitHub returned an invalid or empty repository")
	}
	return r, token, nil
}
func (g *github) repositories(ctx context.Context) ([]githubRepo, error) {
	jwt, err := g.jwt()
	if err != nil {
		return nil, err
	}
	var i struct {
		ID int64 `json:"id"`
	}
	if err = g.request(ctx, "GET", "/orgs/"+g.cfg.Organization+"/installation", jwt, nil, &i); err != nil {
		return nil, err
	}
	token, err := g.installToken(ctx, i.ID, nil)
	if err != nil {
		return nil, err
	}
	all := []githubRepo{}
	for page := 1; page <= 50; page++ {
		var out struct {
			Repositories []githubRepo `json:"repositories"`
		}
		if err = g.request(ctx, "GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), token, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Repositories...)
		if len(out.Repositories) < 100 {
			break
		}
	}
	return all, nil
}
