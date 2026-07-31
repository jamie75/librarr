package organize

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/jamie75/librarr/internal/config"
)

func TestParseAudioFilename(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		artist string
		album  string
		title  string
	}{
		{
			"artist dash title",
			"/music/Bob Dylan - Blowin in the Wind.mp3",
			"Bob Dylan",
			"",
			"Blowin in the Wind",
		},
		{
			"artist dash album dash title",
			"/music/Pink Floyd - The Wall - Another Brick.mp3",
			"Pink Floyd",
			"The Wall",
			"Another Brick",
		},
		{
			"no dash pattern",
			"/music/simple_track.mp3",
			"",
			"",
			"simple_track",
		},
		{
			"with extension stripped",
			"/music/Artist - Title.flac",
			"Artist",
			"",
			"Title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := parseAudioFilename(tt.path)
			if meta == nil {
				t.Fatal("expected non-nil meta")
			}
			if meta.Artist != tt.artist {
				t.Errorf("artist = %q, want %q", meta.Artist, tt.artist)
			}
			if meta.Album != tt.album {
				t.Errorf("album = %q, want %q", meta.Album, tt.album)
			}
			if meta.Title != tt.title {
				t.Errorf("title = %q, want %q", meta.Title, tt.title)
			}
		})
	}
}

func TestExtractAudioMeta_NonMP3(t *testing.T) {
	// For non-MP3 files, it should fall back to filename parsing
	meta := ExtractAudioMeta("/some/path/Artist - Album - Track.ogg")
	if meta == nil {
		t.Fatal("expected non-nil meta")
	}
	if meta.Artist != "Artist" {
		t.Errorf("expected artist 'Artist', got %q", meta.Artist)
	}
	if meta.Album != "Album" {
		t.Errorf("expected album 'Album', got %q", meta.Album)
	}
	if meta.Title != "Track" {
		t.Errorf("expected title 'Track', got %q", meta.Title)
	}
}

func TestExtractAudioMeta_NonexistentMP3(t *testing.T) {
	// MP3 file that doesn't exist should fall back to filename parsing
	meta := ExtractAudioMeta("/nonexistent/Artist - Title.mp3")
	if meta == nil {
		t.Fatal("expected non-nil meta from filename fallback")
	}
	if meta.Artist != "Artist" {
		t.Errorf("expected artist 'Artist', got %q", meta.Artist)
	}
}

func TestExtractAudiobookPathMetadataUsesParentAuthorAndStripsPartSuffix(t *testing.T) {
	meta := ExtractAudiobookPathMetadata(filepath.Join("/books", "audiobooks", "Stephen King", "11.22.63 - Part.m4b"))
	if meta == nil {
		t.Fatal("expected metadata")
	}
	if meta.Artist != "Stephen King" {
		t.Fatalf("artist = %q, want Stephen King", meta.Artist)
	}
	if meta.Title != "11.22.63" {
		t.Fatalf("title = %q, want 11.22.63", meta.Title)
	}
}

func TestExtractAudiobookPathMetadataStripsAuthorPrefix(t *testing.T) {
	meta := ExtractAudiobookPathMetadata(filepath.Join("/books", "audiobooks", "Stephen King", "Stephen King - The Stand - Part 01.m4b"))
	if meta.Artist != "Stephen King" {
		t.Fatalf("artist = %q, want Stephen King", meta.Artist)
	}
	if meta.Title != "The Stand" {
		t.Fatalf("title = %q, want The Stand", meta.Title)
	}
}

func TestAudiobookFilenameFallbackCleansKnownReleaseNoise(t *testing.T) {
	tests := []struct{ name, title, author string }{
		{"Unfreedom of the Press(Unabridged)e.mp3", "Unfreedom of the Press", ""},
		{"American Marxism by Mark R Levin [ENG M4B].m4b", "American Marxism", "Mark R Levin"},
		{"Title - Author Name - Narrator Name - Unabridged.mp3", "Title", "Author Name"},
		{"Title [MP3 64kbps].mp3", "Title", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := ExtractAudiobookPathMetadata(filepath.Join(t.TempDir(), "incoming", tt.name))
			if meta.Title != tt.title || meta.Artist != tt.author {
				t.Fatalf("metadata = %+v, want title=%q author=%q", meta, tt.title, tt.author)
			}
		})
	}
}

func TestExtractM4BMetadataReadsDurationAndChapters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.m4b")
	// mvhd version 0: version/flags, creation, modification, timescale, duration.
	mvhd := make([]byte, 20)
	binary.BigEndian.PutUint32(mvhd[12:16], 1000)
	binary.BigEndian.PutUint32(mvhd[16:20], 125000)
	chpl := make([]byte, 9)
	chpl[8] = 12
	text := make([]byte, 8)
	text = append(text, []byte("Recorded Book")...)
	data := append(mp4Atom("mvhd", mvhd), mp4Atom("chpl", chpl)...)
	data = append(data, mp4Atom(string([]byte{0xa9, 'n', 'a', 'm'}), mp4Atom("data", text))...)
	file := mp4Atom("moov", data)
	if err := os.WriteFile(path, file, 0600); err != nil {
		t.Fatal(err)
	}
	meta := ExtractAudioMeta(path)
	if meta == nil || meta.Title != "Recorded Book" || meta.DurationSeconds != 125 || meta.ChapterCount != 12 {
		t.Fatalf("meta = %+v", meta)
	}
}

func mp4Atom(name string, body []byte) []byte {
	data := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(data[:4], uint32(len(data)))
	copy(data[4:8], []byte(name))
	copy(data[8:], body)
	return data
}

func TestExtractAudioMetaFromDir_NonexistentDir(t *testing.T) {
	meta := ExtractAudioMetaFromDir("/nonexistent/path")
	if meta != nil {
		t.Error("expected nil for nonexistent directory")
	}
}

func TestExtractAudiobookMetadataGroupsTaggedMP3Tracks(t *testing.T) {
	dir := t.TempDir()
	writeTaggedMP3(t, filepath.Join(dir, "01.mp3"), "The Audio Book", "Jane Author", 1)
	writeTaggedMP3(t, filepath.Join(dir, "02.mp3"), "The Audio Book", "Jane Author", 2)
	meta := ExtractAudiobookMetadata(dir)
	if meta == nil || meta.Title != "The Audio Book" || meta.Author != "Jane Author" {
		t.Fatalf("aggregate = %+v", meta)
	}
	if meta.TrackCount != 2 || len(meta.Tracks) != 2 || !meta.Embedded {
		t.Fatalf("tracks = %+v", meta)
	}
}

func TestExtractAudioMetaReadsAPICCover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "covered.mp3")
	writeTaggedMP3WithCover(t, path)
	meta := ExtractAudioMeta(path)
	if meta == nil || meta.Cover == nil || meta.Cover.Source != "embedded_audiobook" {
		t.Fatalf("meta = %+v", meta)
	}
}

func writeTaggedMP3(t *testing.T, path, album, artist string, track int) {
	t.Helper()
	frames := append(id3TextFrame("TALB", album), id3TextFrame("TPE1", artist)...)
	frames = append(frames, id3TextFrame("TRCK", string(rune('0'+track)))...)
	data := append([]byte("ID3\x04\x00\x00"), syncSafe(len(frames))...)
	data = append(data, frames...)
	data = append(data, bytes.Repeat([]byte{0}, 32)...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeTaggedMP3WithCover(t *testing.T, path string) {
	t.Helper()
	image := []byte("\x89PNG\r\n\x1a\ncover")
	frame := append([]byte{0, 'i', 'm', 'a', 'g', 'e', '/', 'p', 'n', 'g', 0, 3, 0}, image...)
	frames := id3Frame("APIC", frame)
	data := append([]byte("ID3\x04\x00\x00"), syncSafe(len(frames))...)
	data = append(data, frames...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func id3TextFrame(name, value string) []byte {
	return id3Frame(name, append([]byte{3}, []byte(value)...))
}
func id3Frame(name string, payload []byte) []byte {
	frame := make([]byte, 10+len(payload))
	copy(frame, name)
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[10:], payload)
	return frame
}
func syncSafe(value int) []byte {
	return []byte{byte(value >> 21), byte(value >> 14), byte(value >> 7), byte(value)}
}

func TestOrganizeAudiobookMissingSourceDoesNotCreateDestDir(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		FileOrgEnabled: true,
		AudiobookDir:   filepath.Join(root, "audiobooks"),
	}
	o := NewOrganizer(cfg)

	missing := filepath.Join(root, "incoming", "missing.m4b")
	_, err := o.OrganizeAudiobook(missing, "Missing Book", "Missing Author")
	if err == nil {
		t.Fatal("expected error")
	}

	destDir := filepath.Join(cfg.AudiobookDir, "Missing Author", "Missing Book")
	if _, statErr := os.Stat(destDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected no dest dir on failure, stat err=%v", statErr)
	}
}

func TestOrganizeAudiobookMovesNestedTreeRecursively(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "incoming", "book")
	cd1 := filepath.Join(src, "CD1")
	cd2 := filepath.Join(src, "CD2", "Extras")

	if err := os.MkdirAll(cd1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cd2, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cd1, "track01.m4b"), []byte("cd1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cd2, "track02.m4b"), []byte("cd2"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		FileOrgEnabled: true,
		AudiobookDir:   filepath.Join(root, "audiobooks"),
	}
	o := NewOrganizer(cfg)

	dest, err := o.OrganizeAudiobook(src, "Nested Book", "Recursive Author")
	if err != nil {
		t.Fatalf("organize failed: %v", err)
	}

	wantRoot := filepath.Join(cfg.AudiobookDir, "Recursive Author", "Nested Book")
	if dest != wantRoot {
		t.Fatalf("dest = %q, want %q", dest, wantRoot)
	}

	if _, err := os.Stat(filepath.Join(wantRoot, "CD1", "track01.m4b")); err != nil {
		t.Fatalf("expected CD1 track at destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wantRoot, "CD2", "Extras", "track02.m4b")); err != nil {
		t.Fatalf("expected nested track at destination: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected source tree removed, stat err=%v", err)
	}
}

func TestOrganizeAudiobookSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "incoming", "book")
	outside := filepath.Join(root, "outside.txt")

	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "track01.m4b"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "linked.m4b")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cfg := &config.Config{
		FileOrgEnabled: true,
		AudiobookDir:   filepath.Join(root, "audiobooks"),
	}
	o := NewOrganizer(cfg)

	dest, err := o.OrganizeAudiobook(src, "Symlink Book", "Careful Author")
	if err != nil {
		t.Fatalf("organize failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "track01.m4b")); err != nil {
		t.Fatalf("expected regular track at destination: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "linked.m4b")); !os.IsNotExist(err) {
		t.Fatalf("expected symlink skipped, lstat err=%v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("expected symlink target left untouched: %v", err)
	}
	if string(data) != "outside" {
		t.Fatalf("outside file = %q, want %q", data, "outside")
	}
}

func TestOrganizeAudiobookRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	realSrc := filepath.Join(root, "incoming", "book")
	linkSrc := filepath.Join(root, "incoming", "linked-book")

	if err := os.MkdirAll(realSrc, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realSrc, "track01.m4b"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSrc, linkSrc); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cfg := &config.Config{
		FileOrgEnabled: true,
		AudiobookDir:   filepath.Join(root, "audiobooks"),
	}
	o := NewOrganizer(cfg)

	_, err := o.OrganizeAudiobook(linkSrc, "Linked Book", "Careful Author")
	if err == nil {
		t.Fatal("expected symlink source error")
	}

	destDir := filepath.Join(cfg.AudiobookDir, "Careful Author", "Linked Book")
	if _, err := os.Stat(destDir); !os.IsNotExist(err) {
		t.Fatalf("expected no destination for symlink source, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(realSrc, "track01.m4b")); err != nil {
		t.Fatalf("expected symlink target left untouched: %v", err)
	}
	if _, err := os.Lstat(linkSrc); err != nil {
		t.Fatalf("expected symlink source left untouched: %v", err)
	}
}
