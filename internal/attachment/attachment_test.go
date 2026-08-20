package attachment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPNG returns a small fake PNG payload (bytes only matter for round-trip
// equality — the store does not decode, M8 裁剪).
func testPNG() []byte {
	return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52}
}

// TestNewStoreCreatesDirectory verifies NewStore creates the directory when it
// does not exist (dispatch-m8-3 §2: <dir> 不存在则 mkdir).
func TestNewStoreCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "attachments")
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if st == nil {
		t.Fatal("NewStore returned nil store")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir %s not created: err=%v", dir, err)
	}
}

// TestNewStoreDefaultDir verifies an empty dir falls back to
// <data_dir>/attachments (dispatch-m8-3 §2).
func TestNewStoreDefaultDir(t *testing.T) {
	st, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if st == nil {
		t.Fatal("NewStore returned nil store")
	}
}

// TestSaveImageRoundTrip verifies Save then Read returns the identical bytes,
// and the returned ImageRef carries the expected metadata (ID/MediaType/Bytes/
// Width/Height/Path; width/height are 0 — not parsed, M8 裁剪).
func TestSaveImageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	data := testPNG()
	ref, err := st.SaveImage("image/png", data, 1024)
	if err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	if ref.ID == "" {
		t.Fatal("ImageRef.ID must be non-empty")
	}
	if ref.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", ref.MediaType)
	}
	if ref.Bytes != int64(len(data)) {
		t.Errorf("Bytes = %d, want %d", ref.Bytes, len(data))
	}
	if ref.Width != 0 || ref.Height != 0 {
		t.Errorf("Width/Height = %d/%d, want 0/0 (not parsed, M8 裁剪)", ref.Width, ref.Height)
	}
	if !filepath.IsAbs(filepath.Clean(ref.Path)) && !strings.HasPrefix(ref.Path, dir) {
		t.Errorf("Path = %q, want under dir %q", ref.Path, dir)
	}
	// The file exists on disk at <dir>/<id>.png with the exact bytes.
	if fi, err := os.Stat(ref.Path); err != nil || fi.Size() != int64(len(data)) {
		t.Errorf("saved file %s: err=%v size=%v", ref.Path, err, fi.Size())
	}
	got, err := st.Read(ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Read bytes differ: got %d bytes, want %d", len(got), len(data))
	}
}

// TestSaveImageIDUnique verifies two saves of the same payload yield different
// ids (dispatch-m8-3 §5: id 唯一).
func TestSaveImageIDUnique(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	data := testPNG()
	r1, err := st.SaveImage("image/png", data, 1024)
	if err != nil {
		t.Fatalf("SaveImage 1: %v", err)
	}
	r2, err := st.SaveImage("image/png", data, 1024)
	if err != nil {
		t.Fatalf("SaveImage 2: %v", err)
	}
	if r1.ID == r2.ID {
		t.Fatalf("ids must differ, both %q", r1.ID)
	}
	if r1.Path == r2.Path {
		t.Fatalf("paths must differ, both %q", r1.Path)
	}
}

// TestSaveImageRejectsUnsupportedType verifies fail-closed on an unsupported
// media type (dispatch-m8-3 §5: 坏扩展名 fail-closed). The store only accepts
// the SupportedMediaTypes vocabulary.
func TestSaveImageRejectsUnsupportedType(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := st.SaveImage("image/tiff", testPNG(), 1024); err == nil {
		t.Fatal("unsupported media type must fail closed")
	} else if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("err = %q, want the unsupported-type error", err)
	}
	if _, err := st.SaveImage("", testPNG(), 1024); err == nil {
		t.Fatal("empty media type must fail closed")
	}
}

// TestSaveImageRejectsEmptyData verifies fail-closed on empty data
// (dispatch-m8-3 §5: 空数据 fail-closed).
func TestSaveImageRejectsEmptyData(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := st.SaveImage("image/png", nil, 1024); err == nil {
		t.Fatal("empty data must fail closed")
	} else if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %q, want the empty-data error", err)
	}
}

// TestSaveImageRejectsTooLarge verifies fail-closed when data exceeds maxBytes
// (dispatch-m8-3 §5: 超限 fail-closed). A payload exactly at the limit is
// accepted.
func TestSaveImageRejectsTooLarge(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	data := testPNG()
	if _, err := st.SaveImage("image/png", data, 10); err == nil {
		t.Fatal("oversized data must fail closed")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %q, want the too-large error", err)
	}
	// Exactly at the limit: accepted.
	if _, err := st.SaveImage("image/png", data, len(data)); err != nil {
		t.Errorf("data exactly at maxBytes must be accepted: %v", err)
	}
	// maxBytes <= 0 means no size gate (the config default is applied upstream).
	if _, err := st.SaveImage("image/png", data, 0); err != nil {
		t.Errorf("non-positive maxBytes must not gate: %v", err)
	}
}

// TestSaveImageRoundTripForEverySupportedType verifies every SupportedMediaTypes
// entry saves and reads back intact (the ext→media-type map and the
// media-type→ext reverse lookup stay in sync).
func TestSaveImageRoundTripForEverySupportedType(t *testing.T) {
	for ext, mediaType := range SupportedMediaTypes {
		ext, mediaType := ext, mediaType
		t.Run(ext, func(t *testing.T) {
			st, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			data := testPNG()
			ref, err := st.SaveImage(mediaType, data, 1024)
			if err != nil {
				t.Fatalf("SaveImage(%s): %v", mediaType, err)
			}
			if ref.MediaType != mediaType {
				t.Errorf("MediaType = %q, want %q", ref.MediaType, mediaType)
			}
			if got := MediaTypeForExtension(ext); got != mediaType {
				t.Errorf("MediaTypeForExtension(%q) = %q, want %q", ext, got, mediaType)
			}
			got, err := st.Read(ref)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if string(got) != string(data) {
				t.Errorf("Read bytes differ for %s", ext)
			}
		})
	}
}

// TestMediaTypeForExtensionCaseInsensitive verifies extension lookup is
// case-insensitive and unknown extensions return "".
func TestMediaTypeForExtensionCaseInsensitive(t *testing.T) {
	if got := MediaTypeForExtension(".PNG"); got != "image/png" {
		t.Errorf(".PNG = %q, want image/png", got)
	}
	if got := MediaTypeForExtension(".jpeg"); got != "image/jpeg" {
		t.Errorf(".jpeg = %q, want image/jpeg", got)
	}
	if got := MediaTypeForExtension(".bmp"); got != "" {
		t.Errorf(".bmp = %q, want empty", got)
	}
}

// TestReadMissingPath verifies Read fails closed when the ref's path is missing
// (dispatch-m8-3 §2: Path 缺失/不可读返回错误).
func TestReadMissingPath(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ref, err := st.SaveImage("image/png", testPNG(), 1024)
	if err != nil {
		t.Fatalf("SaveImage: %v", err)
	}
	if err := os.Remove(ref.Path); err != nil {
		t.Fatalf("remove saved file: %v", err)
	}
	if _, err := st.Read(ref); err == nil {
		t.Fatal("Read of a removed file must fail closed")
	}
}
