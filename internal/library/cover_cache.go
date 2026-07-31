package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jamie75/librarr/internal/organize"
	"github.com/jamie75/librarr/internal/safepath"
)

// CoverCache stores user-owned extracted artwork as files under /data (or the
// configured data directory) and records lightweight cover metadata in SQLite.
type CoverCache struct {
	dir         string
	sourceRoots []string
}

func NewCoverCache(dir string) *CoverCache {
	cleanDir := filepath.Clean(dir)
	if strings.TrimSpace(dir) != "" {
		_ = os.MkdirAll(cleanDir, 0755)
	}
	return &CoverCache{dir: cleanDir}
}

func (c *CoverCache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// SetSourceRoots constrains source files used for cover extraction to the
// configured application roots. An empty list retains the local compatibility
// behavior used by isolated library tests.
func (c *CoverCache) SetSourceRoots(roots ...string) {
	if c == nil {
		return
	}
	c.sourceRoots = append([]string(nil), roots...)
}

func (c *CoverCache) ExtractForScan(jobID, candidateID, sourcePath string) (string, error) {
	if c == nil || strings.TrimSpace(c.dir) == "" {
		return "", nil
	}
	cover, err := c.extractLocalCover(sourcePath)
	if err != nil || cover == nil {
		return "", err
	}
	targetDir := filepath.Join(c.dir, "scan", safeCoverSegment(jobID))
	targetPath := filepath.Join(targetDir, safeCoverSegment(candidateID)+cover.Ext)
	validatedTarget, err := safepath.UnderRoot(c.dir, targetPath)
	if err != nil {
		return "", err
	}
	if err := writeCoverFile(validatedTarget, cover.Data); err != nil {
		return "", err
	}
	return validatedTarget, nil
}

func (c *CoverCache) extractLocalCover(sourcePath string) (*organize.ExtractedCover, error) {
	validatedPath := ""
	var err error
	for _, root := range c.sourceRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if validatedPath, err = safepath.ExistingUnderRoot(root, sourcePath); err == nil {
			break
		}
		validatedPath = ""
	}
	if validatedPath == "" && len(c.sourceRoots) == 0 {
		validatedPath, err = safepath.ExistingUnderRoot(filepath.Dir(sourcePath), sourcePath)
	}
	if validatedPath == "" {
		return nil, err
	}
	if info, err := os.Stat(validatedPath); err == nil && info.IsDir() {
		meta := organize.ExtractAudiobookMetadata(validatedPath)
		if meta != nil && meta.Cover != nil {
			return meta.Cover, nil
		}
		return nil, nil
	}
	return organize.ExtractEmbeddedCover(validatedPath)
}

func (c *CoverCache) AttachBookCover(ctx context.Context, svc *LibraryService, bookID int64, sourcePath string) (*Cover, error) {
	if c == nil || svc == nil || bookID == 0 || strings.TrimSpace(c.dir) == "" {
		return nil, nil
	}
	if existing, err := svc.GetPrimaryCover(ctx, bookID); err == nil && usableCover(existing) {
		return existing, nil
	}
	cover, err := c.extractLocalCover(sourcePath)
	if err != nil {
		return nil, err
	}
	if cover == nil {
		return nil, nil
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", bookID, sourcePath)))
	filename := hex.EncodeToString(sum[:12]) + cover.Ext
	targetPath := filepath.Join(c.dir, "books", filename)
	validatedTarget, err := safepath.UnderRoot(c.dir, targetPath)
	if err != nil {
		return nil, err
	}
	if err := writeCoverFile(validatedTarget, cover.Data); err != nil {
		return nil, err
	}
	attached, err := svc.AttachCover(ctx, Cover{
		BookID:    bookID,
		Source:    cover.Source,
		LocalPath: validatedTarget,
		MimeType:  cover.MimeType,
		Width:     cover.Width,
		Height:    cover.Height,
		IsPrimary: true,
	})
	if err != nil {
		return nil, err
	}
	slog.Debug("attached local book cover", "book_id", bookID, "source", cover.Source, "mime_type", cover.MimeType)
	return attached, nil
}

func usableCover(cover *Cover) bool {
	if cover == nil {
		return false
	}
	if strings.TrimSpace(cover.SourceURL) != "" {
		return true
	}
	localPath := strings.TrimSpace(cover.LocalPath)
	if localPath == "" {
		return false
	}
	info, err := os.Stat(localPath)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func writeCoverFile(targetPath string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("cover image is empty")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("create cover cache directory: %w", err)
	}
	return os.WriteFile(targetPath, data, 0644)
}

func safeCoverSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\x00", "")
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), "-")
	return strings.Trim(value, ".-")
}
