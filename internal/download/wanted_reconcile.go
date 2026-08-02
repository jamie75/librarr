package download

import (
	"strconv"
	"strings"

	"github.com/jamie75/librarr/internal/db"
	"github.com/jamie75/librarr/internal/library"
	libraryimport "github.com/jamie75/librarr/internal/library/import"
)

// reconcileWantedImport is called only after the import engine reports a
// successful or duplicate/idempotent result. Failed, skipped, and conflicted
// imports must never satisfy a Wanted record.
func reconcileWantedImport(database *db.DB, sourceID, title, author string, mediaType library.MediaType, result *libraryimport.EngineResult) {
	if database == nil {
		return
	}
	identity := db.WantedImportIdentity{
		SourceID:  strings.TrimSpace(sourceID),
		Title:     title,
		Author:    author,
		MediaType: string(mediaType),
	}
	if strings.HasPrefix(strings.ToLower(identity.SourceID), "wanted:") {
		if id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(identity.SourceID), "wanted:"), 10, 64); err == nil && id > 0 {
			identity.WantedID = id
		}
	}
	if result != nil && result.Execution != nil {
		for _, execution := range result.Execution.Results {
			if execution.Status == libraryimport.ExecutionStatusSuccess || execution.Status == libraryimport.ExecutionStatusDuplicate {
				identity.LibraryBookID = execution.BookID
				break
			}
		}
	}
	if _, _, err := database.CompleteWantedForImport(identity); err != nil {
		// Completion is bookkeeping. Do not turn a successful library import
		// into a failed download because reconciliation could not be recorded.
		return
	}
}
