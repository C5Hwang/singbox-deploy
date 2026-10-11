package subserve

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

const testSalt = "subserve-salt"

// publishTestSubscriptions renders a real node's subscription files and auth
// key cipher key, so the Tailscale cases run against exactly what Nginx would
// otherwise serve.
func publishTestSubscriptions(t *testing.T) (paths.Layout, string) {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	return layout, publishWithSalt(t, layout, testSalt)
}

func publishWithSalt(t *testing.T, layout paths.Layout, salt string) string {
	t.Helper()
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatalf("GenerateCredentials: %v", err)
	}
	cfg := deploy.Config{
		Domain:               "example.com",
		Ports:                config.Ports{RealityVision: 7443, RealityGRPC: 8443, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		DisplayName:          "US-vps1",
		Salt:                 salt,
		RealityServerName:    "www.microsoft.com",
		RealityHandshakePort: config.DefaultRealityHandshakePort,
		SubscribePort:        deploy.DefaultSubscribePort,
		Creds:                creds,
	}
	if err := deploy.WriteSubscriptions(layout, cfg); err != nil {
		t.Fatalf("WriteSubscriptions: %v", err)
	}
	return deploy.SubscriptionToken(salt)
}

// newHandler is the handler Serve builds for the test layout.
func newHandler(layout paths.Layout) Handler {
	return Handler{SubscribeDir: layout.SubscribeDir, KeysDir: layout.SubscribeKeysDir}
}

// encryptAuthKey seals an auth key the way the dashboard does for a link.
func encryptAuthKey(t *testing.T, salt, hostname, authKey string) string {
	t.Helper()
	enc, err := subscription.EncryptAuthKey(subscription.AuthKeyCipherKey(salt), hostname, authKey)
	if err != nil {
		t.Fatalf("EncryptAuthKey: %v", err)
	}
	return enc
}

// captureLog collects what the handler logs for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func readPublishedFile(t *testing.T, layout paths.Layout, dir, token string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(layout.SubscribeDir, dir, token))
	if err != nil {
		t.Fatalf("read published %s file: %v", dir, err)
	}
	return body
}

func assertCommonHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
}

// Without a "tailscale" parameter the answer is the published file, byte for
// byte, so a client's cache-busting parameter sees what Nginx would serve.
func TestServesPublishedFileUnchangedWithoutTailscaleQuery(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	for _, dir := range []string{"default", "clashMeta", "clashMetaProfiles", "singboxProfiles", "surge", "surgeProfiles"} {
		for _, query := range []string{"", "?t=1700000000", "?flag=meta&_=1"} {
			rec := serve(h, http.MethodGet, "/s/"+dir+"/"+token+query)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s%s = %d: %s", dir, query, rec.Code, rec.Body)
			}
			assertCommonHeaders(t, rec)
			if want := readPublishedFile(t, layout, dir, token); !bytes.Equal(rec.Body.Bytes(), want) {
				t.Fatalf("GET %s%s did not return the published file unchanged", dir, query)
			}
		}
	}
}

func TestHeadAnswersHeadersOnly(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	rec := serve(newHandler(layout), http.MethodHead, "/s/singboxProfiles/"+token+"?t=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD wrote a body of %d bytes", rec.Body.Len())
	}
	want := len(readPublishedFile(t, layout, "singboxProfiles", token))
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(want) {
		t.Fatalf("Content-Length = %q, want %d", got, want)
	}
}

func TestRejectsOtherMethods(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		rec := serve(h, method, "/s/singboxProfiles/"+token+"?tailscale=server")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("%s Allow = %q", method, got)
		}
	}
}

// Only /s/<known dir>/<md5 hex> reaches the disk. Anything that could walk out
// of the subscription directory is a plain 404, even when the file it names
// exists.
func TestRejectsPathsOutsideThePublishedFiles(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	secret := filepath.Join(layout.Root, "secret")
	if err := os.WriteFile(secret, []byte("do not serve"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHandler(layout)
	for _, target := range []string{
		"/s/../x",
		"/s/../secret",
		"/s/singboxProfiles/../../etc/passwd",
		"/s/singboxProfiles/../../secret",
		"/s/singboxProfiles/..%2f..%2fsecret",
		"/s/singboxProfiles/%2e%2e/" + token,
		"/s/singboxProfiles/" + token + "/extra",
		"/s/singboxProfiles/" + strings.ToUpper(token),
		"/s/singboxProfiles/" + token[:31],
		"/s/singboxProfiles/" + token[:31] + "g",
		"/s/singboxProfiles/",
		"/s/state/" + token,
		"/s/sing-boxProfiles/" + token,
		"/s/" + token,
		"/x/singboxProfiles/" + token,
		"/singboxProfiles/" + token,
		"/",
	} {
		for _, query := range []string{"", "?tailscale=server"} {
			rec := serve(h, http.MethodGet, target+query)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET %s%s = %d, want 404", target, query, rec.Code)
			}
			if strings.Contains(rec.Body.String(), "do not serve") {
				t.Fatalf("GET %s%s leaked a file outside the subscription directory", target, query)
			}
		}
	}
}

func TestMissingTokenIsNotFound(t *testing.T) {
	layout, _ := publishTestSubscriptions(t)
	other := deploy.SubscriptionToken("another-salt")
	rec := serve(newHandler(layout), http.MethodGet, "/s/singboxProfiles/"+other+"?tailscale=server")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", rec.Code)
	}
}

func TestTailscaleModeIsOnlyForFullProfiles(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	for _, dir := range []string{"default", "clashMeta", "surge"} {
		rec := serve(h, http.MethodGet, "/s/"+dir+"/"+token+"?tailscale=server")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("tailscale on %s = %d, want 400", dir, rec.Code)
		}
	}
}

func TestInvalidTailscaleQueriesAreRejectedWithoutEchoingTheKey(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	const key = "tskey-auth-SECRETVALUE123"
	enc := encryptAuthKey(t, testSalt, "laptop", key)
	logged := captureLog(t)
	for _, query := range []string{
		"tailscale=bogus",
		"tailscale=client",
		"tailscale=client&hostname=laptop",
		"tailscale=client&authkey=" + enc,
		"tailscale=client&hostname=bad_host!&authkey=" + enc,
		"tailscale=client&hostname=-laptop&authkey=" + enc,
		"tailscale=client&hostname=laptop&authkey=not-a-key",
		"tailscale=client&hostname=laptop&authkey=" + key,
		"tailscale=client&hostname=laptop&authkey=" + enc + "&authkey=" + enc,
		"tailscale=client&hostname=laptop&authkey=" + enc + "%3D",
		"tailscale=client&hostname=laptop&authkey=" + strings.Repeat(enc, 6),
		"tailscale=server&hostname=laptop",
		"tailscale=server&authkey=" + enc,
		"tailscale=server&tailscale=server",
	} {
		rec := serve(h, http.MethodGet, "/s/singboxProfiles/"+token+"?"+query)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("?%s = %d, want 400: %s", query, rec.Code, rec.Body)
		}
		if body := rec.Body.String(); strings.Contains(body, "SECRETVALUE") || strings.Contains(body, enc[:20]) {
			t.Fatalf("?%s echoed the auth key: %s", query, rec.Body)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("?%s answered 400 without a message", query)
		}
	}
	if out := logged.String(); strings.Contains(out, "SECRETVALUE") || strings.Contains(out, enc[:20]) {
		t.Fatalf("the handler logged an auth key: %s", out)
	}
}

func TestMalformedTailscaleQueryIsRejected(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	for _, query := range []string{"tailscale=%zz", "tailscale=server;x=1", "t=1&tailscale=server&x=100%", "a;tailscale=server", "%74ailscale=server&x=%zz"} {
		rec := serve(h, http.MethodGet, "/s/singboxProfiles/"+token+"?"+query)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("?%s = %d, want 400", query, rec.Code)
		}
	}
}

// Go rejects ";" separators and stray "%" escapes, but a link that does not
// ask for Tailscale mode still gets the file Nginx used to serve for it.
func TestMalformedPlainQueryServesThePublishedFile(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	want := readPublishedFile(t, layout, "singboxProfiles", token)
	for _, query := range []string{"a=1;b=2", "a;b", "t=1;2", "x=%zz", "flag=clash&x=100%"} {
		rec := serve(h, http.MethodGet, "/s/singboxProfiles/"+token+"?"+query)
		if rec.Code != http.StatusOK {
			t.Fatalf("?%s = %d, want 200: %s", query, rec.Code, rec.Body)
		}
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("?%s did not return the published file unchanged", query)
		}
	}
}

func TestTailscaleModeRewritesEveryProfileFormat(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	for _, dir := range []string{"clashMetaProfiles", "singboxProfiles", "surgeProfiles"} {
		base := readPublishedFile(t, layout, dir, token)
		for _, query := range []string{
			"tailscale=server",
			"tailscale=client&hostname=laptop&authkey=" + encryptAuthKey(t, testSalt, "laptop", testAuthKey),
		} {
			rec := serve(h, http.MethodGet, "/s/"+dir+"/"+token+"?"+query)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s ?%s = %d: %s", dir, query, rec.Code, rec.Body)
			}
			assertCommonHeaders(t, rec)
			if bytes.Equal(rec.Body.Bytes(), base) {
				t.Fatalf("%s ?%s returned the base profile unchanged", dir, query)
			}
			// The link carries the key encrypted; the profile carries it plain.
			if strings.HasPrefix(query, "tailscale=client") && !bytes.Contains(rec.Body.Bytes(), []byte(testAuthKey)) {
				t.Fatalf("%s client profile does not embed the decrypted auth key", dir)
			}
		}
		// The published file itself is never rewritten.
		if !bytes.Equal(readPublishedFile(t, layout, dir, token), base) {
			t.Fatalf("serving %s in tailscale mode modified the published file", dir)
		}
	}
}

const testAuthKey = "tskey-auth-k123_ABC-def"

func clientQuery(enc string) string { return "?tailscale=client&hostname=laptop&authkey=" + enc }

// The dashboard's own link, built with the shared test vector's scheme, opens
// to a profile that carries the auth key in the clear.
func TestClientLinkDecryptsTheAuthKeyIntoTheProfile(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	rec := serve(newHandler(layout), http.MethodGet, "/s/singboxProfiles/"+token+clientQuery(encryptAuthKey(t, testSalt, "laptop", testAuthKey)))
	if rec.Code != http.StatusOK {
		t.Fatalf("client = %d: %s", rec.Code, rec.Body)
	}
	for _, want := range []string{`"auth_key": "` + testAuthKey + `"`, `"hostname": "laptop"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("client profile lacks %s", want)
		}
	}
}

// A client link opens only for the subscription and hostname it was issued
// for, and a plaintext key is refused rather than served. The 400 explains
// what to do without echoing the value.
func TestClientAuthKeyMustBeEncryptedForThisSubscriptionAndHostname(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	logged := captureLog(t)
	const plain = "tskey-auth-kSECRETVALUE1CNTRL-abcdefghijklmnopqrstuvwxyz0123456789"
	laptop := encryptAuthKey(t, testSalt, "laptop", plain)
	for name, tc := range map[string]struct{ query, want string }{
		"other salt":       {clientQuery(encryptAuthKey(t, "another-salt", "laptop", plain)), "does not belong to this subscription"},
		"other hostname":   {clientQuery(encryptAuthKey(t, testSalt, "desktop", plain)), "does not belong to this subscription"},
		"hostname swapped": {"?tailscale=client&hostname=desktop&authkey=" + laptop, "does not belong to this subscription"},
		"plaintext key":    {clientQuery(plain), "must be encrypted"},
		"bad format":       {clientQuery("v1.!!!" + strings.Repeat("A", 60)), "must be encrypted"},
		"no version":       {clientQuery(strings.TrimPrefix(laptop, "v1.")), "must be encrypted"},
	} {
		rec := serve(h, http.MethodGet, "/s/singboxProfiles/"+token+tc.query)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", name, rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.want) || !strings.Contains(body, "dashboard") {
			t.Fatalf("%s answered %q", name, body)
		}
		if strings.Contains(body, "SECRETVALUE") || strings.Contains(body, laptop[3:23]) {
			t.Fatalf("%s echoed the auth key: %q", name, body)
		}
	}
	if strings.Contains(logged.String(), "SECRETVALUE") {
		t.Fatalf("the handler logged an auth key: %s", logged)
	}
}

// Re-salting a subscription republishes it under a new token and sweeps the
// old token's files and key, so an old client link can no longer reveal the
// auth key: there is neither a profile nor a key left to open it with.
func TestClientLinkStopsOpeningOnceTheSubscriptionIsDestroyed(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	h := newHandler(layout)
	target := "/s/singboxProfiles/" + token + clientQuery(encryptAuthKey(t, testSalt, "laptop", testAuthKey))
	if rec := serve(h, http.MethodGet, target); rec.Code != http.StatusOK {
		t.Fatalf("before = %d: %s", rec.Code, rec.Body)
	}
	publishWithSalt(t, layout, "fresh-salt")
	if _, err := os.Stat(filepath.Join(layout.SubscribeKeysDir, token)); !os.IsNotExist(err) {
		t.Fatalf("the old token's key file survived the re-salt: %v", err)
	}
	rec := serve(h, http.MethodGet, target)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), testAuthKey) {
		t.Fatalf("after = %d: %s", rec.Code, rec.Body)
	}
}

// A profile without its key file belongs to a node that has not regenerated
// its subscriptions since upgrading: the link may work later, so it is a 503,
// and server links, which need no key, keep working.
func TestMissingKeyFileIsUnavailable(t *testing.T) {
	layout, token := publishTestSubscriptions(t)
	if err := os.Remove(filepath.Join(layout.SubscribeKeysDir, token)); err != nil {
		t.Fatal(err)
	}
	enc := encryptAuthKey(t, testSalt, "laptop", testAuthKey)
	for name, h := range map[string]Handler{
		"key file removed": newHandler(layout),
		"no keys dir":      {SubscribeDir: layout.SubscribeDir},
	} {
		rec := serve(h, http.MethodGet, "/s/surgeProfiles/"+token+clientQuery(enc))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: client = %d, want 503: %s", name, rec.Code, rec.Body)
		}
		if rec.Header().Get("Retry-After") == "" || strings.Contains(rec.Body.String(), enc[3:23]) {
			t.Fatalf("%s: client answered %v %q", name, rec.Header(), rec.Body)
		}
		if rec := serve(h, http.MethodGet, "/s/surgeProfiles/"+token+"?tailscale=server"); rec.Code != http.StatusOK {
			t.Fatalf("%s: server = %d: %s", name, rec.Code, rec.Body)
		}
	}
}

// An unknown token is a 404 before the query is even looked at, so a client
// link reveals nothing more about a token than a plain link does.
func TestUnknownTokenIsNotFoundBeforeTheAuthKeyIsChecked(t *testing.T) {
	layout, _ := publishTestSubscriptions(t)
	other := deploy.SubscriptionToken("another-salt")
	enc := encryptAuthKey(t, "another-salt", "laptop", testAuthKey)
	rec := serve(newHandler(layout), http.MethodGet, "/s/singboxProfiles/"+other+clientQuery(enc))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", rec.Code)
	}
}

// A key file that cannot be read or parsed is the server's fault: a generic
// 500, with neither the link's value nor the key in the answer or the log.
func TestUnreadableKeyFileIsAnInternalErrorWithoutTheKey(t *testing.T) {
	enc := encryptAuthKey(t, testSalt, "laptop", testAuthKey)
	keyHex := strings.TrimSpace(string(subscription.AuthKeyCipherKeyFile(testSalt)))
	for name, breakKey := range map[string]func(path string) error{
		"truncated": func(path string) error { return os.WriteFile(path, []byte(keyHex[:40]+"\n"), 0o644) },
		"directory": func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Mkdir(path, 0o755)
		},
	} {
		t.Run(name, func(t *testing.T) {
			layout, token := publishTestSubscriptions(t)
			logged := captureLog(t)
			if err := breakKey(filepath.Join(layout.SubscribeKeysDir, token)); err != nil {
				t.Fatal(err)
			}
			rec := serve(newHandler(layout), http.MethodGet, "/s/singboxProfiles/"+token+clientQuery(enc))
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != "internal server error\n" {
				t.Fatalf("broken key file = %d %q, want a generic 500", rec.Code, rec.Body)
			}
			for _, out := range []string{rec.Body.String(), logged.String()} {
				if strings.Contains(out, enc) || strings.Contains(out, testAuthKey) || strings.Contains(out, keyHex[:40]) {
					t.Fatalf("broken key file leaked a secret: %s", out)
				}
			}
		})
	}
}
