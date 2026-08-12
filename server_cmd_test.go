package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Build a file that looks like an entry nginx wrote: a binary header, then the
// "\nKEY: <key>\n" line the purge relies on, then the cached response.
func cacheEntry(key string) []byte {
	var buf bytes.Buffer
	// Stand in for ngx_http_file_cache_header_t. Any byte but LF works, as the
	// header is opaque to us and only the KEY line is parsed.
	for i := 0; i < 56; i++ {
		buf.WriteByte(byte(i%9) + 1)
	}
	buf.WriteString("\nKEY: " + key + "\n")
	buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
	return buf.Bytes()
}

// Populate a cache with a known set of keys, writing each entry where nginx
// would put it: hashed name under levels=1:2 directories. Returns the cache
// path and the file each key was written to.
func newCache(t *testing.T, keys ...string) (string, map[string]string) {
	t.Helper()
	cachePath := t.TempDir()
	paths := make(map[string]string, len(keys))
	for _, key := range keys {
		sum := md5.Sum([]byte(key))
		name := hex.EncodeToString(sum[:])
		dir := filepath.Join(cachePath, name[31:], name[29:31])
		require.NoError(t, os.MkdirAll(dir, 0o755))
		paths[key] = filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(paths[key], cacheEntry(key), 0o644))
	}
	return cachePath, paths
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// The handler reaches the purge through the package global, as the CLI wires it.
func withApp(t *testing.T) {
	t.Helper()
	previous := app
	app = new(App)
	t.Cleanup(func() { app = previous })
}

// Send a purge to a given server, for the tests that need one configured.
func purgeRawTo(t *testing.T, cmd *ServerCmd, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/?"+query, nil)
	w := httptest.NewRecorder()
	cmd.ServeHTTP(w, req)
	return w
}

// Send a purge with the query string exactly as given, so a test can pass a key
// the way nginx does rather than the way url.Values would escape it.
func purgeRaw(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	return purgeRawTo(t, new(ServerCmd), query)
}

func purgeRequest(t *testing.T, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return purgeRaw(t, query.Encode())
}

func TestPurgeByKey(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		purged []string
		kept   []string
	}{
		{
			name:   "exact key",
			key:    "example.com/index.html",
			purged: []string{"example.com/index.html"},
			kept:   []string{"example.com/img/logo.png", "other.com/index.html"},
		},
		{
			// The glob has no separator, so it spans path segments in the key.
			name:   "wildcard",
			key:    "example.com/*",
			purged: []string{"example.com/index.html", "example.com/img/logo.png"},
			kept:   []string{"other.com/index.html"},
		},
		{
			// A key that is not in the cache is not a failure; there is simply
			// nothing to remove, the common case when purges race each other.
			name: "key not in cache",
			key:  "example.com/absent.html",
			kept: []string{"example.com/index.html", "example.com/img/logo.png", "other.com/index.html"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withApp(t)
			cachePath, paths := newCache(t,
				"example.com/index.html",
				"example.com/img/logo.png",
				"other.com/index.html",
			)

			w := purgeRequest(t, url.Values{"path": {cachePath}, "key": {test.key}})

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, "PURGED", w.Body.String())
			for _, key := range test.purged {
				require.False(t, exists(t, paths[key]), "%s was not purged", key)
			}
			for _, key := range test.kept {
				require.True(t, exists(t, paths[key]), "%s was purged", key)
			}
		})
	}
}

func TestPurgeExcludes(t *testing.T) {
	withApp(t)
	cachePath, paths := newCache(t,
		"example.com/index.html",
		"example.com/keep.html",
		"example.com/img/logo.png",
	)

	// Nginx repeats the parameter to pass more than one exclude.
	w := purgeRequest(t, url.Values{
		"path":    {cachePath},
		"key":     {"example.com/*"},
		"exclude": {"example.com/keep.html", "example.com/img/*"},
	})

	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, exists(t, paths["example.com/index.html"]), "matching key was not purged")
	require.True(t, exists(t, paths["example.com/keep.html"]), "literal exclude was purged")
	require.True(t, exists(t, paths["example.com/img/logo.png"]), "glob exclude was purged")
}

// Nginx substitutes $request_uri into the query string without escaping it, so
// the cache key it stored is the one on the wire byte for byte. Decoding it
// would look for a key nginx never wrote, and answer PURGED having done
// nothing. A caller escaping the key properly still has its purge land, so both
// conventions work over the same socket.
func TestPurgeKeyEncoding(t *testing.T) {
	tests := []struct {
		name string
		key  string
		sent string
	}{
		{"percent escape as sent", "example.com/caf%C3%A9.html", "example.com/caf%C3%A9.html"},
		{"plus sign as sent", "example.com/a+b.html", "example.com/a+b.html"},
		{"percent sign as sent", "example.com/100%.html", "example.com/100%.html"},
		{"escaped by the caller", "example.com/café.html", "example.com/caf%C3%A9.html"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withApp(t)
			cachePath, paths := newCache(t, test.key, "other.com/index.html")

			w := purgeRaw(t, "path="+url.QueryEscape(cachePath)+"&key="+test.sent)

			require.Equal(t, http.StatusOK, w.Code)
			require.False(t, exists(t, paths[test.key]), "key was not purged")
			require.True(t, exists(t, paths["other.com/index.html"]), "unrelated key was purged")
		})
	}
}

// Excludes carry the same ambiguity as the key, and an exclude asks for a key
// to be kept, so either reading of one has to be enough to keep it.
func TestPurgeExcludeEncoding(t *testing.T) {
	for _, exclude := range []string{"example.com/a+b.html", "example.com/a%2Bb.html"} {
		t.Run(exclude, func(t *testing.T) {
			withApp(t)
			cachePath, paths := newCache(t, "example.com/a+b.html", "example.com/index.html")

			w := purgeRaw(t, "path="+url.QueryEscape(cachePath)+"&key=example.com/*&exclude="+exclude)

			require.Equal(t, http.StatusOK, w.Code)
			require.True(t, exists(t, paths["example.com/a+b.html"]), "excluded key was purged")
			require.False(t, exists(t, paths["example.com/index.html"]), "matching key was not purged")
		})
	}
}

// Nginx writes partial responses to temporary files in the same tree. They have
// no cache header, so they must be left alone rather than read as entries, and
// a KEY line past the header scan belongs to a cached body rather than a header.
func TestPurgeLeavesNonEntriesAlone(t *testing.T) {
	withApp(t)
	cachePath, paths := newCache(t, "example.com/index.html")

	// Longer than the scanner's buffer with no line break: the shape of a
	// body-only temporary file.
	temporary := filepath.Join(cachePath, "0000000123")
	require.NoError(t, os.WriteFile(temporary, bytes.Repeat([]byte{'a'}, maxHeaderScan*2), 0o644))
	// An empty file is the other thing a half-written entry looks like.
	empty := filepath.Join(cachePath, "0000000124")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))
	// Short lines, so the scanner keeps reading until the limit cuts it off.
	var body bytes.Buffer
	for body.Len() < maxHeaderScan {
		body.WriteString("filler line\n")
	}
	body.WriteString("KEY: example.com/deep.html\n")
	deep := filepath.Join(cachePath, "0000000125")
	require.NoError(t, os.WriteFile(deep, body.Bytes(), 0o644))

	w := purgeRequest(t, url.Values{"path": {cachePath}, "key": {"*"}})

	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, exists(t, paths["example.com/index.html"]), "matching key was not purged")
	require.True(t, exists(t, temporary), "temporary file was purged")
	require.True(t, exists(t, empty), "empty file was purged")
	require.True(t, exists(t, deep), "file whose KEY line is past the header scan was purged")
}

// A cache key built from a request URI carries the punctuation that decides
// whether a key is read as a pattern: a query string puts ? in the key, and
// PHP-style array parameters put [ and ]. Neither was meant as a wildcard, and
// read as one they purge the wrong thing, fail to compile, or match nothing
// while still answering PURGED. Exact says the key is a literal, which is the
// only way those entries can be purged at all.
func TestPurgeExactKey(t *testing.T) {
	keys := []string{
		"example.com/index.html",
		"example.com/page.html?id=5",
		"example.com/list.php?f[]=x",
		"example.com/{weird}",
		// A key that holds what would otherwise be a wildcard purging the lot.
		"example.com/*",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			withApp(t)
			cachePath, paths := newCache(t, key, "other.com/index.html")

			// Sent unescaped, the way the Nginx rewrite substitutes it.
			w := purgeRaw(t, "path="+url.QueryEscape(cachePath)+"&exact=1&key="+key)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, "PURGED", w.Body.String())
			require.False(t, exists(t, paths[key]), "key was not purged")
			require.True(t, exists(t, paths["other.com/index.html"]), "unrelated key was purged")
		})
	}
}

// Without exact those same keys are patterns, which is what the parameter
// exists to turn off. The wildcard purging everything under it is the case that
// makes reading a literal key as a pattern dangerous rather than merely wrong.
func TestPurgeWithoutExactReadsKeyAsPattern(t *testing.T) {
	withApp(t)
	cachePath, paths := newCache(t, "example.com/*", "other.com/index.html")

	w := purgeRaw(t, "path="+url.QueryEscape(cachePath)+"&key=example.com/*")

	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, exists(t, paths["example.com/*"]), "the pattern matched its own key")
	require.True(t, exists(t, paths["other.com/index.html"]), "unrelated key was purged")
}

// An exclude carries the same punctuation as a key, so an exact purge reads it
// literally too. Compiled as a pattern, an exclude holding [ fails the whole
// purge it appears in.
func TestPurgeExactExcludes(t *testing.T) {
	const excluded = "example.com/list.php?f[]=x"

	withApp(t)
	cachePath, paths := newCache(t, excluded, "example.com/other.php")
	query := "path=" + url.QueryEscape(cachePath) + "&key=example.com/*&exclude=" + excluded

	// As a pattern the exclude will not compile, and a purge that cannot honour
	// an exclude must not run.
	w := purgeRawTo(t, new(ServerCmd), query)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.True(t, exists(t, paths[excluded]), "purged despite the failure")

	// Exact reads both the key and the exclude literally, so the key named by
	// the exclude is the one kept.
	w = purgeRawTo(t, new(ServerCmd), query+"&exact=1")
	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, exists(t, paths[excluded]), "excluded key was purged")
	// The key is a literal too, so it purges only itself and not the sibling.
	require.True(t, exists(t, paths["example.com/other.php"]), "unrelated key was purged")
}

// A value that is not a boolean has to be refused. Falling back to pattern
// matching on a typo is how a key stops being purged while the response still
// reports that it was.
func TestPurgeExactValues(t *testing.T) {
	tests := []struct {
		value string
		exact bool
		bad   bool
	}{
		{value: "1", exact: true},
		{value: "true", exact: true},
		{value: "0", exact: false},
		{value: "false", exact: false},
		// Present with no value at all still asks for it.
		{value: "", exact: true},
		{value: "yes", bad: true},
		{value: "2", bad: true},
	}
	for _, test := range tests {
		t.Run("exact="+test.value, func(t *testing.T) {
			withApp(t)
			// Read as a pattern this key is an alternation that matches
			// nothing on disk, so only an exact purge removes it.
			const key = "example.com/{weird}"
			cachePath, paths := newCache(t, key)

			parameter := "&exact"
			if test.value != "" {
				parameter += "=" + test.value
			}
			w := purgeRaw(t, "path="+url.QueryEscape(cachePath)+parameter+"&key="+key)

			if test.bad {
				require.Equal(t, http.StatusBadRequest, w.Code)
				require.True(t, exists(t, paths[key]), "purged despite the bad parameter")
				return
			}
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, test.exact, !exists(t, paths[key]), "key purged with exact=%v", test.exact)
		})
	}
}

// A missing parameter has to come back as a failure status. Answering 200 with
// an error in the body reads to nginx as a successful purge.
func TestPurgeRejectsMissingParameters(t *testing.T) {
	withApp(t)
	cachePath, _ := newCache(t)

	tests := []struct {
		name  string
		query url.Values
	}{
		{"no path", url.Values{"key": {"example.com/*"}}},
		{"no key", url.Values{"path": {cachePath}}},
		{"empty path", url.Values{"path": {""}, "key": {"example.com/*"}}},
		{"empty key", url.Values{"path": {cachePath}, "key": {""}}},
		{"neither", url.Values{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := purgeRequest(t, test.query)
			require.Equal(t, http.StatusBadRequest, w.Code)
			require.NotEqual(t, "PURGED", w.Body.String())
		})
	}
}

// A purge that could not be carried out has to say so, rather than answer
// PURGED and leave the caller believing the keys are gone. An exclude that
// cannot compile is the sharpest case: carrying on would delete the very keys
// the caller asked to keep.
func TestPurgeReportsFailure(t *testing.T) {
	tests := []struct {
		name       string
		absentPath bool
		key        string
		exclude    string
	}{
		{name: "cache path does not exist", absentPath: true, key: "example.com/*"},
		{name: "key glob will not compile", key: "example.com/[a-"},
		{name: "exclude glob will not compile", key: "example.com/*", exclude: "example.com/[a-"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withApp(t)
			cachePath, paths := newCache(t, "example.com/index.html")

			query := url.Values{"path": {cachePath}, "key": {test.key}}
			if test.absentPath {
				query.Set("path", filepath.Join(cachePath, "absent"))
			}
			if test.exclude != "" {
				query.Set("exclude", test.exclude)
			}
			w := purgeRequest(t, query)

			require.Equal(t, http.StatusInternalServerError, w.Code)
			require.NotEqual(t, "PURGED", w.Body.String())
			require.True(t, exists(t, paths["example.com/index.html"]), "purged despite the failure")
		})
	}
}

// The path to purge comes from the caller, and the purge deletes what it finds
// under it, so a server given cache directories has to serve no others.
func TestServerAllowsCachePath(t *testing.T) {
	base := t.TempDir()
	allowed := filepath.Join(base, "cache")
	// A sibling whose name starts with the allowed one, which a plain string
	// prefix would take for a directory inside it.
	sibling := filepath.Join(base, "cache-other")
	require.NoError(t, os.MkdirAll(allowed, 0o755))
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(allowed, link))

	tests := []struct {
		name  string
		roots []string
		path  string
		want  bool
	}{
		// A server without the flag purges wherever it is told, as it always has.
		{"no allowlist", nil, sibling, true},
		{"the named directory", []string{allowed}, allowed, true},
		{"a directory inside it", []string{allowed}, filepath.Join(allowed, "inner"), true},
		{"an unclean spelling of it", []string{allowed}, filepath.Join(allowed, "inner", ".."), true},
		{"one of several", []string{sibling, allowed}, allowed, true},
		// Symlinks are resolved, so a link to the cache is the cache whichever
		// side of the comparison it is written on.
		{"a symlink to it", []string{allowed}, link, true},
		{"named by a symlink to it", []string{link}, allowed, true},
		{"a sibling sharing its name", []string{allowed}, sibling, false},
		{"the directory above it", []string{allowed}, base, false},
		{"an unrelated directory", []string{allowed}, t.TempDir(), false},
		{"the root", []string{allowed}, "/", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := &ServerCmd{CachePaths: test.roots}
			require.Equal(t, test.want, cmd.allows(test.path))
		})
	}
}

// A path the server will not purge has to be refused outright, rather than
// walked and reported on.
func TestPurgeRefusesCachePathOutsideAllowlist(t *testing.T) {
	withApp(t)
	cachePath, paths := newCache(t, "example.com/index.html")

	cmd := &ServerCmd{CachePaths: []string{t.TempDir()}}
	w := purgeRawTo(t, cmd, "path="+url.QueryEscape(cachePath)+"&key=*")

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NotEqual(t, "PURGED", w.Body.String())
	require.True(t, exists(t, paths["example.com/index.html"]), "purged from a path that is not allowed")
}

// Connecting to a UNIX socket needs write permission on it, so the mode has to
// be set rather than left to the umask the service manager started us with, and
// a mode that cannot be used has to stop the server before it takes the socket
// path over.
func TestListenSocketMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want os.FileMode
	}{
		{"default", "", 0o660},
		{"from the command line", "0666", 0o666},
		{"not a number", "junk", 0},
		{"not octal", "0999", 0},
		{"out of range", "1777", 0},
		{"negative", "-1", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := &ServerCmd{SocketMode: test.mode}
			socket := filepath.Join(t.TempDir(), "http.sock")

			listener, err := cmd.listen(socket)
			if test.want == 0 {
				require.Error(t, err)
				require.False(t, exists(t, socket), "socket was bound despite the mode being rejected")
				return
			}
			require.NoError(t, err)
			defer listener.Close()

			info, err := os.Lstat(socket)
			require.NoError(t, err)
			require.Equal(t, test.want, info.Mode().Perm())
		})
	}
}

func TestListenSocketPath(t *testing.T) {
	// The socket directory may not exist yet, and net.Listen will not create it.
	t.Run("creates the socket directory", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "run", "http.sock")

		listener, err := new(ServerCmd).listen(socket)
		require.NoError(t, err)
		defer listener.Close()

		info, err := os.Lstat(socket)
		require.NoError(t, err, "socket was not created")
		require.NotZero(t, info.Mode()&os.ModeSocket, "path exists but is not a socket")
	})

	// A socket left behind by a killed run must not stop the next one binding.
	t.Run("replaces a stale socket", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "http.sock")

		// Closing normally unlinks the file, so keep it to look like a crash.
		stale, err := net.Listen("unix", socket)
		require.NoError(t, err)
		stale.(*net.UnixListener).SetUnlinkOnClose(false)
		require.NoError(t, stale.Close())
		require.True(t, exists(t, socket), "stale socket was not left behind")

		listener, err := new(ServerCmd).listen(socket)
		require.NoError(t, err, "listen over stale socket")
		require.NoError(t, listener.Close())
	})

	// Taking over a socket another instance is serving would silently steal its
	// requests, so binding has to fail instead.
	t.Run("refuses a live socket", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "http.sock")

		live, err := net.Listen("unix", socket)
		require.NoError(t, err)
		defer live.Close()

		_, err = new(ServerCmd).listen(socket)
		require.Error(t, err)
		require.True(t, exists(t, socket), "live socket was removed")
	})

	// A path holding something other than a socket is not ours to unlink.
	t.Run("refuses a path that is not a socket", func(t *testing.T) {
		socket := filepath.Join(t.TempDir(), "http.sock")
		require.NoError(t, os.WriteFile(socket, nil, 0o644))

		_, err := new(ServerCmd).listen(socket)
		require.Error(t, err)
		require.True(t, exists(t, socket), "the existing file was removed")
	})
}

// The whole command: bind the socket given on the command line, serve a purge
// over it, and shut down on the signal systemd sends to stop the service.
func TestRunServesAndShutsDown(t *testing.T) {
	withApp(t)
	cachePath, paths := newCache(t, "example.com/index.html", "other.com/index.html")

	socket := filepath.Join(t.TempDir(), "http.sock")
	cmd := &ServerCmd{Socket: socket}

	runErr := make(chan error, 1)
	go func() { runErr <- cmd.Run() }()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}

	// Run binds asynchronously, so wait for the socket to answer.
	var resp *http.Response
	query := url.Values{"path": {cachePath}, "key": {"example.com/*"}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		resp, err = client.Get("http://socket/purge?" + query.Encode())
		if err == nil {
			break
		}
		require.False(t, time.Now().After(deadline), "server never accepted a connection: %s", err)
		time.Sleep(10 * time.Millisecond)
	}

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "PURGED", string(body))
	require.False(t, exists(t, paths["example.com/index.html"]), "matching key was not purged")
	require.True(t, exists(t, paths["other.com/index.html"]), "unrelated key was purged")

	// The command handles SIGTERM itself, so this stops the server rather than
	// the test binary.
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))

	select {
	case err := <-runErr:
		require.NoError(t, err, "run returned an error on shutdown")
	case <-time.After(15 * time.Second):
		t.Fatal("server did not shut down on SIGTERM")
	}

	// Shutting down closes the listener, which unlinks the socket for the run
	// after this one.
	require.False(t, exists(t, socket), "socket file was left behind")
}

// A socket that cannot be bound is the command failing, not the server running
// with nowhere to listen.
func TestRunReportsListenFailure(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "http.sock")
	require.NoError(t, os.WriteFile(socket, nil, 0o644))

	require.Error(t, (&ServerCmd{Socket: socket}).Run())
}
