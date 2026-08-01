package applebooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/library"
)

func newTestExporter(t *testing.T, title string, tracks []string) (*Exporter, *db.DB, string, string, int64) {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "librarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := library.NewNormalizedRepository(database.SQLDB())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	service, err := library.NewLibraryService(library.ServiceOptions{
		BookRepository: repo, EditionRepository: repo, FileRepository: repo,
		MetadataRepository: repo, SeriesRepository: repo, ContributorRepository: repo,
		IdentifierRepository: repo, CoverRepository: repo,
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	book, err := repo.CreateBook(context.Background(), library.Book{Title: title, MediaType: library.MediaTypeAudiobook})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	edition, err := repo.CreateEdition(context.Background(), library.Edition{BookID: book.ID, Title: title})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := repo.AttachContributor(context.Background(), edition.ID, library.Contributor{Name: "Test Author", Roles: []library.ContributorRole{library.RoleAuthor}}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range tracks {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			database.Close()
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("audio-"+name), 0600); err != nil {
			database.Close()
			t.Fatal(err)
		}
		if _, err := repo.AttachFile(context.Background(), library.BookFile{
			EditionID: edition.ID, MediaType: library.MediaTypeAudiobook,
			Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), "."), Path: path,
			Size: int64(len("audio-" + name)), Managed: true,
		}); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	exportRoot := t.TempDir()
	exporter := NewExporter(service, database, Config{Enabled: true, ExportDir: exportRoot, AudiobookRoot: root})
	return exporter, database, root, exportRoot, book.ID
}

func newTestEbookExporter(t *testing.T, title, author, filename string, content []byte) (*Exporter, *db.DB, string, string, int64) {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "librarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := library.NewNormalizedRepository(database.SQLDB())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	service, err := library.NewLibraryService(library.ServiceOptions{
		BookRepository: repo, EditionRepository: repo, FileRepository: repo,
		MetadataRepository: repo, SeriesRepository: repo, ContributorRepository: repo,
		IdentifierRepository: repo, CoverRepository: repo,
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	book, err := repo.CreateBook(context.Background(), library.Book{Title: title, MediaType: library.MediaTypeEbook})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	edition, err := repo.CreateEdition(context.Background(), library.Edition{BookID: book.ID, Title: title})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if author != "" {
		if err := repo.AttachContributor(context.Background(), edition.ID, library.Contributor{Name: author, Roles: []library.ContributorRole{library.RoleAuthor}}); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, filename)
	if err := os.WriteFile(path, content, 0600); err != nil {
		database.Close()
		t.Fatal(err)
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if _, err := repo.AttachFile(context.Background(), library.BookFile{
		EditionID: edition.ID, MediaType: library.MediaTypeEbook, Format: ext, Path: path,
		Size: int64(len(content)), Managed: true,
	}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	exportRoot := t.TempDir()
	exporter := NewExporter(service, database, Config{Enabled: true, ExportDir: exportRoot, EbookRoot: root})
	return exporter, database, root, exportRoot, book.ID
}

func TestExportSingleM4BAndHistory(t *testing.T) {
	exporter, database, _, exportRoot, bookID := newTestExporter(t, "A Book", []string{"book.m4b"})
	defer database.Close()
	record, err := exporter.Export(context.Background(), bookID, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusCompleted || record.ActualFormat != "m4b" {
		t.Fatalf("record = %+v", record)
	}
	data, err := os.ReadFile(filepath.Join(exportRoot, "Test Author - A Book.m4b"))
	if err != nil || string(data) != "audio-book.m4b" {
		t.Fatalf("exported data = %q, err=%v", data, err)
	}
	history, err := database.ListAppleBooksExports(bookID, 10)
	if err != nil || len(history) != 1 || history[0].Status != StatusCompleted {
		t.Fatalf("history = %+v, err=%v", history, err)
	}
}

func TestExportMultiTrackMP3IncludesOrderedManifest(t *testing.T) {
	exporter, database, _, exportRoot, bookID := newTestExporter(t, "A Book", []string{"02.mp3", "01.mp3"})
	defer database.Close()
	coverRoot := t.TempDir()
	coverPath := filepath.Join(coverRoot, "cover.jpg")
	if err := os.WriteFile(coverPath, []byte("cover"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().Exec(`INSERT INTO covers (book_id, source, local_path, mime_type, is_primary) VALUES (?, ?, ?, ?, 1)`, bookID, "embedded", coverPath, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	exporter.config.CoverRoot = coverRoot
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(exportRoot, "Test Author - A Book")
	manifest, err := os.ReadFile(filepath.Join(packageDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(manifest, &value); err != nil || value["tracks"] != float64(2) {
		t.Fatalf("manifest = %s, err=%v", manifest, err)
	}
	for _, name := range []string{"001 - 01.mp3", "002 - 02.mp3"} {
		if _, err := os.Stat(filepath.Join(packageDir, name)); err != nil {
			t.Fatalf("missing ordered track %s: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(packageDir, "cover.jpg")); err != nil || string(data) != "cover" {
		t.Fatalf("cover = %q, err=%v", data, err)
	}
}

func TestExportRejectsSourceOutsideAudiobookRoot(t *testing.T) {
	exporter, database, _, _, bookID := newTestExporter(t, "A Book", []string{"book.m4b"})
	defer database.Close()
	outside := filepath.Join(t.TempDir(), "outside.m4b")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().Exec(`UPDATE files SET file_path = ?`, outside); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("export error = %v, want unsafe source error", err)
	}
}

func TestExportEPUBCopiesSourceAndPersistsHistory(t *testing.T) {
	content := []byte("epub bytes")
	exporter, database, sourceRoot, exportRoot, bookID := newTestEbookExporter(t, "The Book", "Author Name", "source.epub", content)
	defer database.Close()
	record, err := exporter.Export(context.Background(), bookID, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusCompleted || record.MediaType != string(library.MediaTypeEbook) || record.ActualFormat != "epub" || record.SourceFileCount != 1 || record.SourceBytes != int64(len(content)) || record.Checksum == "" {
		t.Fatalf("record = %+v", record)
	}
	if got, err := os.ReadFile(filepath.Join(sourceRoot, "source.epub")); err != nil || string(got) != string(content) {
		t.Fatalf("source changed: %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(exportRoot, "Author Name - The Book.epub")); err != nil || string(got) != string(content) {
		t.Fatalf("export = %q, err=%v", got, err)
	}
	history, err := database.ListAppleBooksExports(bookID, 10)
	if err != nil || len(history) != 1 || history[0].MediaType != string(library.MediaTypeEbook) {
		t.Fatalf("history = %+v, err=%v", history, err)
	}
}

func TestExportPDFWithoutAuthorUsesTitleOnly(t *testing.T) {
	exporter, database, _, exportRoot, bookID := newTestEbookExporter(t, "A PDF Book", "", "source.pdf", []byte("pdf bytes"))
	defer database.Close()
	if _, err := exporter.Export(context.Background(), bookID, "pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(exportRoot, "A PDF Book.pdf")); err != nil {
		t.Fatalf("title-only PDF export missing: %v", err)
	}
}

func TestExportEbookRejectsUnsupportedAndUnsafeSources(t *testing.T) {
	for _, format := range []string{"mobi", "azw3"} {
		exporter, database, _, _, bookID := newTestEbookExporter(t, "Unsupported", "Author", "source."+format, []byte(format))
		if _, err := exporter.Export(context.Background(), bookID, "auto"); err == nil || !strings.Contains(err.Error(), "not supported") {
			database.Close()
			t.Fatalf("unsupported %s error = %v", format, err)
		}
		database.Close()
	}

	exporter, database, _, _, bookID := newTestEbookExporter(t, "Manga", "Author", "source.epub", []byte("epub"))
	if _, err := database.SQLDB().Exec(`UPDATE books SET media_type = 'manga' WHERE id = ?`, bookID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err == nil || !strings.Contains(err.Error(), "only audiobooks and ebooks") {
		database.Close()
		t.Fatalf("manga error = %v", err)
	}
	database.Close()

	exporter, database, root, _, bookID := newTestEbookExporter(t, "Unsafe", "Author", "source.epub", []byte("epub"))
	defer database.Close()
	outside := filepath.Join(t.TempDir(), "outside.epub")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().Exec(`UPDATE files SET file_path = ?`, outside); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("outside source error = %v", err)
	}
	link := filepath.Join(root, "escape.epub")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().Exec(`UPDATE files SET file_path = ?`, link); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("symlink source error = %v", err)
	}
}

func TestExportEbookOverwriteDisabled(t *testing.T) {
	exporter, database, _, exportRoot, bookID := newTestEbookExporter(t, "Repeat", "Author", "source.epub", []byte("epub"))
	defer database.Close()
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Export(context.Background(), bookID, "auto"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second export error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportRoot, "Author - Repeat.epub")); err != nil {
		t.Fatalf("first export missing: %v", err)
	}
}

func TestSafeNameAndFormatRules(t *testing.T) {
	if got := safeName("Author/Title: Test"); got != "Author-Title- Test" {
		t.Fatalf("safeName = %q", got)
	}
	if got := chooseFormat("m4b", []sourceTrack{{ext: "mp3"}}); got != "" {
		t.Fatalf("chooseFormat = %q", got)
	}
}
