package gdocs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tanaikech/gdocumentor/internal/utf16idx"
)

const (
	// PreparedSnapshotTTL defines the 30-minute validity window for stateless 2-step MCP / --prepare-only snapshots.
	PreparedSnapshotTTL = 30 * time.Minute
)

var (
	snapshotMu      sync.Mutex
	reSafePathToken = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)
)

// PreparedRevisionSnapshot persists Stage 1 settled structure from a stateless prepareOnly / MCP Phase 1 call
// so Phase 2 (skipDocInsert + aiResult) can deterministically detect concurrent collaborator edits (rev_1 != rev_2)
// and re-anchor duplicate phrases, --range scopes, tables, and VISUAL_N ObjectIDs onto rev_2.
type PreparedRevisionSnapshot struct {
	DocumentID        string                         `json:"documentId"`
	ActiveTabID       string                         `json:"activeTabId,omitempty"`
	RevisionID        string                         `json:"revisionId"`
	ScopeKey          string                         `json:"scopeKey,omitempty"`
	CreatedAtUnixMs   int64                          `json:"createdAtUnixMs"`
	SelectionOnly     bool                           `json:"selectionOnly,omitempty"`
	SelectionRanges   []utf16idx.RangeInterval       `json:"selectionRanges,omitempty"`
	ScopeAnchors      []utf16idx.ScopeBoundaryAnchor `json:"scopeAnchors,omitempty"`
	EditableBlocks    []utf16idx.EditableBlock       `json:"editableBlocks"`
	VisualElements    []VisualElement                `json:"visualElements,omitempty"`
	Tables            []TableElement                 `json:"tables,omitempty"`
	SettledUnits      []uint16                       `json:"settledUnits,omitempty"`
	SettledDocIndices []int                          `json:"settledDocIndices,omitempty"`
}

func preparedSnapshotsDir() (string, error) {
	baseDir := os.Getenv("DOCUMENTOR_CONFIG_DIR")
	if strings.TrimSpace(baseDir) == "" {
		if cfgDir, err := os.UserConfigDir(); err == nil && cfgDir != "" {
			baseDir = filepath.Join(cfgDir, "gdocumentor")
		} else {
			baseDir = filepath.Join(os.TempDir(), "gdocumentor")
		}
	}
	dir := filepath.Join(baseDir, "prepared_snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func sanitizeSnapshotToken(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "default"
	}
	clean := reSafePathToken.ReplaceAllString(trimmed, "_")
	if len(clean) > 96 {
		clean = clean[:96]
	}
	return clean
}

func hashScopeKeyShort(scopeKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(scopeKey)))
	return hex.EncodeToString(sum[:8])
}

func preparedSnapshotPrefix(cleanID, tabID string) string {
	return sanitizeSnapshotToken(cleanID) + "__" + sanitizeSnapshotToken(tabID) + "__"
}

func preparedSnapshotFilePath(docID, tabID, scopeKey string) (string, error) {
	cleanID := ExtractDocumentID(docID)
	if cleanID == "" {
		return "", os.ErrInvalid
	}
	dir, err := preparedSnapshotsDir()
	if err != nil {
		return "", err
	}
	fileName := preparedSnapshotPrefix(cleanID, tabID) + hashScopeKeyShort(scopeKey) + ".json"
	return filepath.Join(dir, fileName), nil
}

// SavePreparedRevisionSnapshot atomically writes an ephemeral Stage 1 snapshot for docData when prepareOnly / MCP Phase 1 runs.
func SavePreparedRevisionSnapshot(docID string, docData *ParsedDocument, scopeKey string) {
	if docData == nil || strings.TrimSpace(docData.RevisionID) == "" {
		return
	}
	cleanID := ExtractDocumentID(docID)
	if cleanID == "" {
		return
	}

	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	pruneExpiredPreparedSnapshotsLocked()

	fp, err := preparedSnapshotFilePath(cleanID, docData.ActiveTabID, scopeKey)
	if err != nil {
		return
	}

	// Strip raw image bytes before serializing
	visCopy := make([]VisualElement, len(docData.VisualElements))
	for i, v := range docData.VisualElements {
		v.ImageBytes = nil
		visCopy[i] = v
	}

	var settledUnits []uint16
	var settledIndices []int
	if docData.SettledBuffer != nil && len(docData.SettledBuffer.Units) == len(docData.SettledBuffer.DocIndices) {
		settledUnits = docData.SettledBuffer.Units
		settledIndices = docData.SettledBuffer.DocIndices
	}

	snap := PreparedRevisionSnapshot{
		DocumentID:        cleanID,
		ActiveTabID:       docData.ActiveTabID,
		RevisionID:        strings.TrimSpace(docData.RevisionID),
		ScopeKey:          strings.TrimSpace(scopeKey),
		CreatedAtUnixMs:   time.Now().UnixMilli(),
		SelectionOnly:     docData.SelectionOnly,
		SelectionRanges:   docData.SelectionRanges,
		ScopeAnchors:      docData.ScopeAnchors,
		EditableBlocks:    docData.EditableBlocks,
		VisualElements:    visCopy,
		Tables:            docData.Tables,
		SettledUnits:      settledUnits,
		SettledDocIndices: settledIndices,
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return
	}

	dir := filepath.Dir(fp)
	tmpFile, err := os.CreateTemp(dir, ".snap-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmpFile.Name()
	_, wErr := tmpFile.Write(data)
	cErr := tmpFile.Close()
	if wErr != nil || cErr != nil {
		_ = os.Remove(tmpPath)
		return
	}
	_ = os.Chmod(tmpPath, 0o600)
	if err := os.Rename(tmpPath, fp); err != nil {
		_ = os.Remove(tmpPath)
	}
}

func readValidSnapshotFile(fp string) *PreparedRevisionSnapshot {
	data, err := os.ReadFile(fp)
	if err != nil || len(data) == 0 {
		return nil
	}
	var snap PreparedRevisionSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}
	age := time.Since(time.UnixMilli(snap.CreatedAtUnixMs))
	if age < 0 || age > PreparedSnapshotTTL {
		_ = os.Remove(fp)
		return nil
	}
	return &snap
}

// LoadPreparedRevisionSnapshot retrieves a valid non-expired Stage 1 snapshot matching docID + tabID.
// When requestedBaseRevisionID is explicitly supplied, it matches that revision ID (checking the exact scopeKey slot
// first and then scanning same-tab snapshots); when omitted, it matches strictly on snap.ScopeKey == scopeKey.
func LoadPreparedRevisionSnapshot(docID, tabID, scopeKey, requestedBaseRevisionID string) *PreparedRevisionSnapshot {
	cleanID := ExtractDocumentID(docID)
	if cleanID == "" {
		return nil
	}

	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	reqRev := strings.TrimSpace(requestedBaseRevisionID)
	fp, err := preparedSnapshotFilePath(cleanID, tabID, scopeKey)
	if err == nil {
		if snap := readValidSnapshotFile(fp); snap != nil {
			if reqRev != "" {
				if snap.RevisionID == reqRev {
					return snap
				}
			} else if strings.TrimSpace(snap.ScopeKey) == strings.TrimSpace(scopeKey) {
				return snap
			}
		}
	}

	if reqRev == "" {
		return nil
	}

	dir, err := preparedSnapshotsDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := preparedSnapshotPrefix(cleanID, tabID)
	var newest *PreparedRevisionSnapshot
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		cand := readValidSnapshotFile(filepath.Join(dir, entry.Name()))
		if cand != nil && cand.RevisionID == reqRev {
			if newest == nil || cand.CreatedAtUnixMs > newest.CreatedAtUnixMs {
				newest = cand
			}
		}
	}
	return newest
}

// ReconstructParsedDocument rebuilds a Stage 1 ParsedDocument (including the full-tab SettledBuffer and ScopeAnchors)
// from a persisted PreparedRevisionSnapshot.
func (s *PreparedRevisionSnapshot) ReconstructParsedDocument() *ParsedDocument {
	if s == nil {
		return nil
	}
	var buf *utf16idx.SettledUTF16Buffer
	if len(s.SettledUnits) > 0 && len(s.SettledUnits) == len(s.SettledDocIndices) {
		buf = &utf16idx.SettledUTF16Buffer{
			Units:      s.SettledUnits,
			DocIndices: s.SettledDocIndices,
		}
	} else {
		buf = utf16idx.BuildSettledBufferFromBlocks(s.EditableBlocks)
	}
	anchors := s.ScopeAnchors
	if s.SelectionOnly && len(s.SelectionRanges) > 0 && len(anchors) == 0 {
		anchors = utf16idx.BuildScopeBoundaryAnchors(s.SelectionRanges, buf)
	}
	return &ParsedDocument{
		DocumentID:      s.DocumentID,
		RevisionID:      s.RevisionID,
		ActiveTabID:     s.ActiveTabID,
		SelectionOnly:   s.SelectionOnly,
		SelectionRanges: s.SelectionRanges,
		ScopeAnchors:    anchors,
		EditableBlocks:  s.EditableBlocks,
		VisualElements:  s.VisualElements,
		Tables:          s.Tables,
		SettledBuffer:   buf,
	}
}

func pruneExpiredPreparedSnapshotsLocked() {
	dir, err := preparedSnapshotsDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-PreparedSnapshotTTL)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
