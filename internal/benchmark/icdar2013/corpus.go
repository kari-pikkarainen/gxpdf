package icdar2013

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// TabulaJavaCommit pins the tabula-java revision the corpus is taken from.
	TabulaJavaCommit = "2cdf3b4fd3f7e921dca8cc6814cdd9316be40f0f"
	maxPDFAssetBytes = 128 << 20
	// maxDocumentBytes bounds one annotation XML file or one worker JSON reply.
	maxDocumentBytes = 32 << 20
)

// readVerifiedBytes reads a bounded file and checks the SHA-256 of exactly the
// bytes it returns.
func readVerifiedBytes(path, id, label, expectedHash string, limit int64) ([]byte, error) {
	data, err := readBoundedFile(path, limit)
	if err != nil {
		return nil, fmt.Errorf("read icdar case %q %s: %w", id, label, err)
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != expectedHash {
		return nil, fmt.Errorf("icdar case %q %s hash mismatch: got %s", id, label, actual)
	}
	return data, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("asset exceeds %d-byte limit", limit)
	}
	return data, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// resolveCorpusAsset turns a slash-separated relative path into a file inside
// root. Absolute paths, ".." components and symlinks leading outside root are
// rejected.
func resolveCorpusAsset(root, relativePath string) (string, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("path must be relative")
	}
	for _, component := range strings.FieldsFunc(filepath.ToSlash(relativePath), func(r rune) bool { return r == '/' }) {
		if component == ".." {
			return "", fmt.Errorf("path traversal is not allowed")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if clean == "." {
		return "", fmt.Errorf("path must name a file")
	}
	rootResolved, err := resolveCorpusRoot(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(rootResolved, clean))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootResolved, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path escapes corpus root")
	}
	return resolved, nil
}

func resolveCorpusRoot(root string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve corpus root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve corpus root: %w", err)
	}
	return rootResolved, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("icdar json has trailing value")
		}
		return fmt.Errorf("decode icdar json trailer: %w", err)
	}
	return nil
}
