package download

import (
	"bytes"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jamie75/librarr/internal/config"
	"github.com/jamie75/librarr/internal/sources/sourcestest"
)

func TestTitlesMatch(t *testing.T) {
	tests := []struct {
		name      string
		expected  string
		candidate string
		want      bool
	}{
		{"same title", "Human Transit: How Clearer Thinking About Public Transit Can Enrich Our Communities and Our Lives", "Human transit : how clearer thinking about public transit can enrich our communities and our lives", true},
		{"short matching title", "Human Transit: How Clearer Thinking About Public Transit Can Enrich Our Communities and Our Lives", "Jarrett Walker - Human Transit.epub", true},
		{"one generic word is insufficient", "Human Transit: How Clearer Thinking About Public Transit Can Enrich Our Communities and Our Lives", "Chasing Molecules: Poisonous Products, Human Health", false},
		{"subtitle fragment is insufficient", "Human Transit: How Clearer Thinking About Public Transit Can Enrich Our Communities and Our Lives", "Public Transit", false},
		{"unrelated", "Human Transit", "The Great Gatsby", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := titlesMatch(tc.expected, tc.candidate); got != tc.want {
				t.Errorf("titlesMatch(%q, %q) = %v, want %v", tc.expected, tc.candidate, got, tc.want)
			}
		})
	}
}

func TestParseAnnasSearchCandidatesKeepsTitlesWithMD5s(t *testing.T) {
	html := `<html><body>
		<a href="/md5/11111111111111111111111111111111"><img alt="cover"></a>
		<a href="/md5/11111111111111111111111111111111">Human Transit</a>
		<a href="/md5/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA">Uppercase MD5</a>
		<a href="/md5/22222222222222222222222222222222">Chasing Molecules</a>
	</body></html>`

	results, err := parseAnnasSearchCandidates(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse candidates: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d candidates, want 3: %+v", len(results), results)
	}
	if results[0].Title != "Human Transit" || results[0].MD5 != "11111111111111111111111111111111" {
		t.Errorf("unexpected first candidate: %+v", results[0])
	}
	if results[1].MD5 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("uppercase MD5 was not normalized: %+v", results[1])
	}
}

func TestDownloadFromLibgenMirrorsContinuesAfterDownloadFailure(t *testing.T) {
	failedDownloads := int32(0)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=book&key=first">GET</a>`))
			return
		}
		atomic.AddInt32(&failedDownloads, 1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer first.Close()

	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{'x'}, 1500)...)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=book&key=second">GET</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	}))
	defer second.Close()

	cfg := newTestConfig([]string{first.URL, second.URL})
	cfg.IncomingDir = t.TempDir()
	d := NewDirectDownloader(cfg, second.Client())
	d.validate = nil

	filePath, fileSize, err := d.downloadFromLibgenMirrors("book", "Human Transit", make(map[string]bool), nil)
	if err != nil {
		t.Fatalf("download from mirrors: %v", err)
	}
	if atomic.LoadInt32(&failedDownloads) != 1 {
		t.Errorf("first mirror download calls = %d, want 1", failedDownloads)
	}
	if !strings.HasSuffix(filePath, ".pdf") || fileSize != int64(len(pdf)) {
		t.Errorf("unexpected downloaded file: path=%q size=%d", filePath, fileSize)
	}
}

func TestDownloadFromLibgenMirrorsContinuesAfterInvalidEPUB(t *testing.T) {
	invalidEPUB := append([]byte{0x50, 0x4B, 0x03, 0x04}, bytes.Repeat([]byte{'x'}, 2000)...)
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{'p'}, 1500)...)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=book">GET</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/epub+zip")
		_, _ = w.Write(invalidEPUB)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=book">GET</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	}))
	defer second.Close()

	cfg := newTestConfig([]string{first.URL, second.URL})
	cfg.IncomingDir = t.TempDir()
	d := NewDirectDownloader(cfg, second.Client())
	d.validate = nil
	path, _, err := d.downloadFromLibgenMirrors("book", "Fallback Book", make(map[string]bool), nil)
	if err != nil {
		t.Fatalf("invalid EPUB fallback: %v", err)
	}
	if !strings.HasSuffix(path, ".pdf") {
		t.Fatalf("fallback path = %q, want PDF", path)
	}
}

func TestDownloadFromLibgenMirrorsStopsAfterVerifiedSuccess(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{'v'}, 1500)...)
	expectedMD5 := fmt.Sprintf("%x", md5.Sum(pdf))
	secondCalls := int32(0)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=` + expectedMD5 + `">GET</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdf)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&secondCalls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer second.Close()

	cfg := newTestConfig([]string{first.URL, second.URL})
	cfg.IncomingDir = t.TempDir()
	d := NewDirectDownloader(cfg, first.Client())
	d.validate = nil

	path, size, err := d.downloadFromLibgenMirrors(expectedMD5, "Verified Book", make(map[string]bool), nil)
	if err != nil {
		t.Fatalf("download from first verified mirror: %v", err)
	}
	if secondCalls != 0 {
		t.Fatalf("later mirror was contacted %d times after success", secondCalls)
	}
	if size != int64(len(pdf)) || path == "" {
		t.Fatalf("unexpected successful download path=%q size=%d", path, size)
	}
}

func TestDownloadFromLibgenMirrorsContinuesAfterChecksumMismatch(t *testing.T) {
	good := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{'g'}, 1500)...)
	wrong := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{'w'}, 1500)...)
	expectedMD5 := fmt.Sprintf("%x", md5.Sum(good))
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=` + expectedMD5 + `">GET</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(wrong)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			_, _ = w.Write([]byte(`<a href="get.php?md5=` + expectedMD5 + `">GET</a>`))
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(good)
	}))
	defer second.Close()

	cfg := newTestConfig([]string{first.URL, second.URL})
	cfg.IncomingDir = t.TempDir()
	d := NewDirectDownloader(cfg, second.Client())
	d.validate = nil
	path, _, err := d.downloadFromLibgenMirrors(expectedMD5, "Checksum Book", make(map[string]bool), nil)
	if err != nil {
		t.Fatalf("checksum fallback: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(contents, good) {
		t.Fatalf("fallback did not retain verified content: err=%v", err)
	}
}

func TestDownloadFromLibgenMirrorsSkipsDuplicateMirrorConfiguration(t *testing.T) {
	adsCalls := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ads.php") {
			atomic.AddInt32(&adsCalls, 1)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cfg := newTestConfig([]string{server.URL, server.URL + "/"})
	d := NewDirectDownloader(cfg, server.Client())
	d.validate = nil
	_, _, err := d.downloadFromLibgenMirrors("not-a-valid-md5", "Duplicate Mirror", make(map[string]bool), nil)
	if err == nil {
		t.Fatal("expected duplicate mirror configuration to fail")
	}
	if got := atomic.LoadInt32(&adsCalls); got != 1 {
		t.Fatalf("duplicate mirror was queried %d times, want 1", got)
	}
}

// newTestConfig builds a Config with the given libgen mirrors injected into
// the runtime sources registry.
func newTestConfig(mirrors []string) *config.Config {
	reg, _ := sourcestest.Registry()
	reg.LibgenMirrors = mirrors
	return &config.Config{UserAgent: "test", Sources: reg}
}

// TestFetchLibgenDownloadURL_FirstMirrorWorks is the happy path.
func TestFetchLibgenDownloadURL_FirstMirrorWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><a href="get.php?md5=abc123&key=XYZ">GET</a></html>`))
	}))
	defer server.Close()

	cfg := newTestConfig([]string{server.URL})
	d := NewDirectDownloader(cfg, server.Client())

	url, err := d.fetchLibgenDownloadURL("abc123", nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !strings.Contains(url, "get.php?md5=abc123") {
		t.Errorf("unexpected URL: %s", url)
	}
}

// TestFetchLibgenDownloadURL_FailsOverOn500 — issue #7 regression test.
// First mirror returns HTTP 500, second mirror succeeds.
func TestFetchLibgenDownloadURL_FailsOverOn500(t *testing.T) {
	brokenCalls := int32(0)
	workingCalls := int32(0)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&brokenCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&workingCalls, 1)
		_, _ = w.Write([]byte(`<a href="get.php?md5=abc&key=ZZZ">GET</a>`))
	}))
	defer working.Close()

	cfg := newTestConfig([]string{broken.URL, working.URL})
	d := NewDirectDownloader(cfg, working.Client())

	url, err := d.fetchLibgenDownloadURL("abc", nil)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got: %v", err)
	}
	if !strings.HasPrefix(url, working.URL+"/") {
		t.Errorf("URL should be from working mirror, got: %s", url)
	}
	if atomic.LoadInt32(&brokenCalls) != 1 {
		t.Errorf("broken mirror should be tried once, got %d", brokenCalls)
	}
	if atomic.LoadInt32(&workingCalls) != 1 {
		t.Errorf("working mirror should be tried once, got %d", workingCalls)
	}
}

// TestFetchLibgenDownloadURL_AllMirrorsFail — all mirrors down.
func TestFetchLibgenDownloadURL_AllMirrorsFail(t *testing.T) {
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer m1.Close()
	m2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer m2.Close()

	cfg := newTestConfig([]string{m1.URL, m2.URL})
	d := NewDirectDownloader(cfg, m1.Client())

	_, err := d.fetchLibgenDownloadURL("abc", nil)
	if err == nil {
		t.Fatal("expected error when all mirrors fail, got nil")
	}
	// Should report the LAST mirror's error
	if !strings.Contains(err.Error(), "HTTP") {
		t.Errorf("error should mention HTTP status: %v", err)
	}
}

func TestFetchLibgenDownloadURL_MixedNoMatchAndServerErrorIsRetryable(t *testing.T) {
	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("File not found in DB"))
	}))
	defer missing.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer broken.Close()

	d := NewDirectDownloader(newTestConfig([]string{missing.URL, broken.URL}), broken.Client())
	_, err := d.fetchLibgenDownloadURL("abc", nil)
	if err == nil {
		t.Fatal("expected mirror resolution failure")
	}
	if errors.Is(err, errLibgenNoMatch) {
		t.Fatalf("mixed transient failure was classified as no-match: %v", err)
	}
}

// TestFetchLibgenDownloadURL_MirrorLacksBook — some mirrors don't have the MD5.
func TestFetchLibgenDownloadURL_MirrorLacksBook(t *testing.T) {
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Returns 200 but no get.php link (book not present on this mirror)
		_, _ = w.Write([]byte(`<html><body>File not found</body></html>`))
	}))
	defer m1.Close()
	m2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a href="get.php?md5=xyz&key=ABC">GET</a>`))
	}))
	defer m2.Close()

	cfg := newTestConfig([]string{m1.URL, m2.URL})
	d := NewDirectDownloader(cfg, m1.Client())

	url, err := d.fetchLibgenDownloadURL("xyz", nil)
	if err != nil {
		t.Fatalf("expected fallback to mirror with the book, got: %v", err)
	}
	if !strings.HasPrefix(url, m2.URL+"/") {
		t.Errorf("URL should be from m2, got: %s", url)
	}
}

// TestFetchLibgenDownloadURL_NoMatchIsRecognized exercises the real LibGen
// response we saw in production: HTML with "File not found in DB".
func TestFetchLibgenDownloadURL_NoMatchIsRecognized(t *testing.T) {
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<div class="alert alert-danger" role="alert">File not found in DB</div>`))
	}))
	defer m1.Close()

	cfg := newTestConfig([]string{m1.URL})
	d := NewDirectDownloader(cfg, m1.Client())

	_, err := d.fetchLibgenDownloadURL("missing-md5", nil)
	if err == nil {
		t.Fatal("expected no-match error, got nil")
	}
	if !errors.Is(err, errLibgenNoMatch) {
		t.Fatalf("expected errLibgenNoMatch, got: %v", err)
	}
}

// TestFetchLibgenDownloadURL_NetworkErrorFailsOver — connection refused on one mirror.
func TestFetchLibgenDownloadURL_NetworkErrorFailsOver(t *testing.T) {
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a href="get.php?md5=abc&key=XXX">GET</a>`))
	}))
	defer working.Close()

	// Point to a port that's closed.
	cfg := newTestConfig([]string{"http://127.0.0.1:1", working.URL})
	d := NewDirectDownloader(cfg, working.Client())

	url, err := d.fetchLibgenDownloadURL("abc", nil)
	if err != nil {
		t.Fatalf("expected fallback on network error, got: %v", err)
	}
	if !strings.HasPrefix(url, working.URL+"/") {
		t.Errorf("URL should be from working mirror, got: %s", url)
	}
}

// TestFetchLibgenDownloadURL_ProgressCallback verifies progress updates.
func TestFetchLibgenDownloadURL_ProgressCallback(t *testing.T) {
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer m1.Close()
	m2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a href="get.php?md5=a&key=B">GET</a>`))
	}))
	defer m2.Close()

	cfg := newTestConfig([]string{m1.URL, m2.URL})
	d := NewDirectDownloader(cfg, m1.Client())

	var messages []string
	_, err := d.fetchLibgenDownloadURL("a", func(msg string) {
		messages = append(messages, msg)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) == 0 {
		t.Error("expected progress messages, got none")
	}
	found := false
	for _, m := range messages {
		if strings.Contains(m, "Trying mirror") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'Trying mirror' message, got: %v", messages)
	}
}

// TestIsAnnasNoMatchError_RecognizesSentinel — the manager must classify a
// no-match error correctly even if the user-facing message is reworded or
// localized. errors.Is on the sentinel is the contract; string matching is a
// fallback for legacy errors round-tripped through the DB.
func TestIsAnnasNoMatchError_RecognizesSentinel(t *testing.T) {
	t.Run("sentinel via noMatchError", func(t *testing.T) {
		err := &noMatchError{msg: "Anything in any language at all"}
		if !isAnnasNoMatchError(err) {
			t.Fatalf("noMatchError must be classified as no-match")
		}
		if !errors.Is(err, errLibgenNoMatch) {
			t.Fatalf("noMatchError must satisfy errors.Is(err, errLibgenNoMatch)")
		}
	})

	t.Run("sentinel via fmt.Errorf %w wrap", func(t *testing.T) {
		wrapped := fmt.Errorf("anna's archive: %w", errLibgenNoMatch)
		if !isAnnasNoMatchError(wrapped) {
			t.Fatalf("fmt.Errorf %%w wrap of errLibgenNoMatch must be classified")
		}
	})

	t.Run("legacy string match still works", func(t *testing.T) {
		// Pre-PR builds emitted plain errors with these phrases. We must keep
		// recognizing them so jobs persisted by the old build still dead-letter
		// correctly after the new build starts up.
		for _, msg := range []string{
			"all libgen mirrors failed: File not found in DB",
			"some message mentioning matching LibGen MD5 in the middle",
			"libgen no matching MD5",
		} {
			if !isAnnasNoMatchError(errors.New(msg)) {
				t.Errorf("legacy phrase should still match: %q", msg)
			}
		}
	})

	t.Run("unrelated errors are not classified", func(t *testing.T) {
		for _, e := range []error{
			nil,
			errors.New("timeout"),
			errors.New("http 500"),
			fmt.Errorf("wrapped: %w", errors.New("connection refused")),
		} {
			if isAnnasNoMatchError(e) {
				t.Errorf("must not classify unrelated error as no-match: %v", e)
			}
		}
	})
}

// TestNoMatchError_FrontendMessagePreserved — the user-facing Error() string
// must be exactly the message the frontend's no-match detector looks for,
// otherwise the sticky toast won't render. This locks in the contract between
// the Go backend and the JS isAnnaNoMatchError() check in web/index.html.
func TestNoMatchError_FrontendMessagePreserved(t *testing.T) {
	e := &noMatchError{msg: "Anna's Archive could not find a matching LibGen MD5 for this book. Download it manually from Anna's Archive or choose another source."}
	got := e.Error()
	if !strings.Contains(got, "matching LibGen MD5") {
		t.Fatalf("Error() must contain 'matching LibGen MD5' for the frontend to recognize: %q", got)
	}
}

// TestLibgenMirrors_Configured ensures the canonical sources registry ships
// multiple mirrors so a single mirror outage doesn't break downloads entirely.
func TestLibgenMirrors_Configured(t *testing.T) {
	reg, err := sourcestest.Registry()
	if err != nil {
		t.Fatalf("load canonical sources registry: %v", err)
	}
	if len(reg.LibgenMirrors) < 3 {
		t.Errorf("should have at least 3 libgen mirrors for redundancy, got %d", len(reg.LibgenMirrors))
	}
	for _, m := range reg.LibgenMirrors {
		if !strings.HasPrefix(m, "http://") && !strings.HasPrefix(m, "https://") {
			t.Errorf("mirror URL missing scheme: %s", m)
		}
	}
}

// Avoid unused-import linter errors when only some tests are built
var _ = fmt.Sprintf
