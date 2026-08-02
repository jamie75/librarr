package api

import (
	"strings"
	"testing"
)

func TestDownloadContentDispositionUsesSafeLegacyAndUTF8Names(t *testing.T) {
	header, err := downloadContentDisposition("Zoë - Book.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header, `filename="Zo_ - Book.pdf"`) {
		t.Fatalf("legacy filename missing: %q", header)
	}
	if !strings.Contains(header, "filename*=UTF-8''Zo%C3%AB%20-%20Book.pdf") {
		t.Fatalf("UTF-8 filename missing: %q", header)
	}
}

func TestDownloadContentDispositionRejectsHeaderInjection(t *testing.T) {
	if _, err := downloadContentDisposition("book\r\nX-Injected: true.pdf"); err == nil {
		t.Fatal("expected CRLF filename to be rejected")
	}
}
