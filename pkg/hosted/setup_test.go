package hosted

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootstrapProtectsSetupAndPersistsCredentials(t *testing.T) {
	var exchanged atomic.Int32
	settings := appSettings{ID: 42, Slug: "ownership-atlas-test", ClientID: "client", ClientSecret: "client-secret", WebhookSecret: strings.Repeat("w", 32), PEM: "-----BEGIN PRIVATE KEY-----\ntest-key\n-----END PRIVATE KEY-----"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/app-manifests/registration-code/conversions" {
			http.NotFound(w, r)
			return
		}
		exchanged.Add(1)
		jsonResponse(w, 201, settings)
	}))
	defer api.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	c := Config{Listen: address, PublicURL: "https://atlas.test", DataDir: t.TempDir(), Organization: "conduktor", EmailDomain: "conduktor.io", Admins: []string{"sderosiaux"}, GitHubAPI: api.URL}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Bootstrap(ctx, c, io.Discard) }()
	client := &http.Client{Timeout: time.Second}
	request := func(path string, cookie *http.Cookie) (*http.Response, string, error) {
		req, _ := http.NewRequest("GET", "http://"+address+path, nil)
		req.Host = "atlas.test"
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp, string(body), err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, _, err := request("/healthz", nil); err == nil && resp.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bootstrap did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/setup", "/setup?token=wrong", "/api/repositories", "/r/conduktor/private/codebase"} {
		resp, _, err := request(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 404 && resp.StatusCode != 503 {
			t.Fatalf("unprotected path %s: %d", path, resp.StatusCode)
		}
	}
	token, err := os.ReadFile(filepath.Join(c.DataDir, "setup-token"))
	if err != nil {
		t.Fatal(err)
	}
	resp, body, err := request("/setup?token="+url.QueryEscape(string(token)), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("setup inaccessible", err)
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("setup token could leak via referrer")
	}
	var cookie *http.Cookie
	for _, v := range resp.Cookies() {
		if v.Name == "atlas_setup" {
			cookie = v
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly {
		t.Fatal("missing secure setup cookie")
	}
	match := regexp.MustCompile(`name="manifest" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing manifest")
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &manifest); err != nil {
		t.Fatal(err)
	}
	permissions := manifest["default_permissions"].(map[string]any)
	for _, name := range []string{"contents", "metadata", "members", "emails"} {
		if permissions[name] != "read" {
			t.Fatal("unexpected permissions", permissions)
		}
	}
	if len(permissions) != 4 || manifest["public"] != false || manifest["redirect_url"] != c.PublicURL+"/setup/callback" {
		t.Fatal("incorrect app manifest")
	}
	callback := "/setup/callback?code=registration-code&state=" + cookie.Value
	if resp, _, err = request(callback, nil); err != nil || resp.StatusCode != 400 || exchanged.Load() != 0 {
		t.Fatal("callback accepted without bound cookie")
	}
	if resp, _, err = request(callback, cookie); err != nil || resp.StatusCode != 200 {
		t.Fatal("registration failed", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not finish")
	}
	if exchanged.Load() != 1 {
		t.Fatal("registration exchanged more than once")
	}
	if _, err = os.Stat(filepath.Join(c.DataDir, "setup-token")); !os.IsNotExist(err) {
		t.Fatal("setup token retained")
	}
	saved := c
	if err = saved.LoadGitHubSettings(); err != nil || !saved.GitHubConfigured() {
		t.Fatal("credentials not restartable", err)
	}
	if saved.AppID != "42" || saved.ClientSecret != settings.ClientSecret {
		t.Fatal("saved settings mismatch")
	}
	for _, name := range []string{"github-app.json", "github-app.pem"} {
		info, err := os.Stat(filepath.Join(c.DataDir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("credentials have unsafe permissions", name, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(c.DataDir, "github-app.json"))
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("private key duplicated in JSON")
	}
	override := c
	override.ClientSecret = "environment-override"
	if err = override.LoadGitHubSettings(); err != nil || override.ClientSecret != "environment-override" {
		t.Fatal("explicit environment lost")
	}
	if err = Bootstrap(context.Background(), c, io.Discard); err == nil {
		t.Fatal("bootstrap allowed reconfiguration")
	}
}
