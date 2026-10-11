// Package subserve is the dynamic side of the public /s/ endpoint. Nginx keeps
// serving every plain subscription link straight from disk; only a request
// that carries a query string is handed to this loopback service, which reads
// the same published file and, for a full profile asked for in Tailscale mode,
// injects the requested Tailscale configuration before answering. A client
// link's auth key arrives encrypted under a key derived from its
// subscription's salt. The service never sees a salt: it reads the derived key
// from the key file published for the link's token, which is deleted together
// with the subscription's files.
package subserve

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// servableDirs are the published format directories, the same set Nginx
// aliases under /s/. Anything else is answered 404 without touching disk.
var servableDirs = map[string]bool{
	"default":           true,
	"clashMeta":         true,
	"clashMetaProfiles": true,
	"singboxProfiles":   true,
	"surge":             true,
	"surgeProfiles":     true,
}

// tokenPattern is the md5 hex digest every published file is named after.
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// tailscaleQueryKey is the parameter that switches a request into Tailscale
// mode. Its presence alone decides whether the directory must be a profile.
const tailscaleQueryKey = "tailscale"

// Handler answers GET and HEAD for /s/<dir>/<token>. A request without a
// "tailscale" parameter gets the published file unchanged, so the
// cache-busting parameters some clients append keep working once Nginx starts
// routing query-bearing links here.
type Handler struct {
	// SubscribeDir is the root the format directories live under, the same
	// directory Nginx aliases /s/ to.
	SubscribeDir string

	// KeysDir holds one file per published token with the key its client
	// links' auth keys are encrypted under (see
	// subscription.AuthKeyCipherKeyFile). It is not served by Nginx.
	KeysDir string
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	// A Tailscale profile embeds an auth key; neither it nor the base file it
	// was built from may be kept by an intermediary.
	header.Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		plainError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir, token, ok := splitSubscriptionPath(r.URL.Path)
	if !ok {
		plainError(w, "not found", http.StatusNotFound)
		return
	}
	// A query Go cannot fully parse (a ";" separator, a stray "%") is only an
	// error when it asks for Tailscale mode; any other link was served
	// whatever its query before Nginx started routing queries here.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil && mentionsTailscale(r.URL.RawQuery) {
		plainError(w, "malformed query string", http.StatusBadRequest)
		return
	}
	format, isProfile := subscription.ProfileFormatForDir(dir)
	if _, tailscale := query[tailscaleQueryKey]; tailscale && !isProfile {
		plainError(w, "tailscale mode is only available for full profiles", http.StatusBadRequest)
		return
	}

	body, err := readPublished(h.SubscribeDir, dir, token)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			plainError(w, "not found", http.StatusNotFound)
			return
		}
		log.Printf("subscription: read %s file: %v", dir, err)
		plainError(w, "internal server error", http.StatusInternalServerError)
		return
	}

	opts, tailscale, err := subscription.ParseTailscaleQuery(query)
	if err != nil {
		// The parser's message names the offending field; the key itself is a
		// credential and must never be reflected back or end up in a log.
		plainError(w, redact(err.Error(), query["authkey"]), http.StatusBadRequest)
		return
	}
	if tailscale && opts.Role == subscription.TailscaleClient {
		key, err := h.authKeyCipherKey(token)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// The profile is published but its key file is not, which
				// only happens on a node that has not regenerated its
				// subscriptions since upgrading (the key file is written
				// first and swept last). That is the server's state, not a
				// bad link, and the next regeneration fixes it.
				header.Set("Retry-After", "60")
				plainError(w, "tailscale client links are not available for this subscription yet; try again later", http.StatusServiceUnavailable)
				return
			}
			log.Printf("subscription: read the auth key cipher key of a %s link: %v", dir, err)
			plainError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		// DecryptAuthKey's errors never carry the value or the plaintext.
		if opts.AuthKey, err = subscription.DecryptAuthKey(key, opts.Hostname, opts.EncryptedAuthKey); err != nil {
			plainError(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if tailscale {
		body, err = subscription.ApplyTailscale(format, body, opts)
		if err != nil {
			log.Printf("subscription: build %s tailscale %s profile: %s", dir, opts.Role, redact(err.Error(), []string{opts.AuthKey, opts.EncryptedAuthKey}))
			plainError(w, "failed to build the tailscale profile", http.StatusInternalServerError)
			return
		}
	}

	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// authKeyCipherKey reads the key a token's client links are encrypted under.
// The token has already matched tokenPattern, so it cannot name a path
// outside KeysDir.
func (h Handler) authKeyCipherKey(token string) ([]byte, error) {
	if h.KeysDir == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(h.KeysDir, token))
	if err != nil {
		return nil, err
	}
	return subscription.ParseAuthKeyCipherKeyFile(data)
}

// splitSubscriptionPath accepts exactly /s/<dir>/<token> with a known format
// directory and a well-formed token. Matching whole segments against an
// allowlist and a hex pattern leaves nothing for "..", an encoded slash or a
// stray extra segment to smuggle into the file path.
func splitSubscriptionPath(path string) (dir, token string, ok bool) {
	rest, found := strings.CutPrefix(path, "/s/")
	if !found {
		return "", "", false
	}
	dir, token, found = strings.Cut(rest, "/")
	if !found || !servableDirs[dir] || !tokenPattern.MatchString(token) {
		return "", "", false
	}
	return dir, token, true
}

// mentionsTailscale reports whether any parameter name in a raw query, decoded
// where it can be, contains the Tailscale key. It is deliberately loose: it
// only decides whether a query that failed to parse is answered 400 or served
// the plain file.
func mentionsTailscale(rawQuery string) bool {
	for _, pair := range strings.Split(rawQuery, "&") {
		key, _, _ := strings.Cut(pair, "=")
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if strings.Contains(key, tailscaleQueryKey) {
			return true
		}
	}
	return false
}

// readPublished reads one published file. A directory in its place reads as
// missing, the same answer Nginx's alias would give for it.
func readPublished(subscribeDir, dir, token string) ([]byte, error) {
	path := filepath.Join(subscribeDir, dir, token)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(path)
}

func redact(message string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return message
}

func plainError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(message + "\n"))
}

// Serve runs the handler for layout's published files and auth key cipher
// keys on listen until ctx is cancelled, then gives in-flight requests a few
// seconds to finish. Nginx is the only client, so the
// timeouts only guard against a stuck connection holding a goroutine forever.
func Serve(ctx context.Context, listen string, layout paths.Layout) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           Handler{SubscribeDir: layout.SubscribeDir, KeysDir: layout.SubscribeKeysDir},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Serve returns as soon as Shutdown stops accepting; wait for the requests
	// already being answered before letting the process exit.
	<-shutdownDone
	return nil
}
