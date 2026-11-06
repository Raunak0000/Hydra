package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ============================================================================
// 1. SYMLINK SAFETY TESTS
// ============================================================================

func TestSymlink_OpenExistingDestination_RejectsDirectSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	link := filepath.Join(dir, "link.bin")

	if err := os.WriteFile(target, []byte("sensitive-data"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, err := OpenExistingDestination(link)
	if err == nil {
		_ = file.Close()
		t.Fatal("expected OpenExistingDestination to reject direct symlink")
	}

	// Verify target was not modified
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "sensitive-data" {
		t.Fatal("target file was modified through symlink")
	}
}

func TestSymlink_OpenExistingDestination_RejectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nonexistent.bin")
	link := filepath.Join(dir, "dangling.bin")

	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, err := OpenExistingDestination(link)
	if err == nil {
		_ = file.Close()
		t.Fatal("expected OpenExistingDestination to reject dangling symlink")
	}
}

func TestSymlink_OpenExistingDestination_RejectsSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	linkA := filepath.Join(dir, "loopA.bin")
	linkB := filepath.Join(dir, "loopB.bin")

	if err := os.Symlink(linkB, linkA); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(linkA, linkB); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, err := OpenExistingDestination(linkA)
	if err == nil {
		_ = file.Close()
		t.Fatal("expected OpenExistingDestination to reject recursive symlink loop")
	}
}

func TestSymlink_ReserveConflict_DoesNotFollowExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	sensitiveFile := filepath.Join(dir, "sensitive.txt")
	if err := os.WriteFile(sensitiveFile, []byte("ORIGINAL_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}

	linkTarget := filepath.Join(dir, "download.bin")
	if err := os.Symlink(sensitiveFile, linkTarget); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Reserving download.bin when download.bin is a symlink should allocate
	// download (1).bin and NOT follow or truncate sensitive.txt
	reservedPath, file, err := ReserveConflict(linkTarget, 0600)
	if err != nil {
		t.Fatalf("ReserveConflict failed: %v", err)
	}
	defer file.Close()

	expectedPath := filepath.Join(dir, "download (1).bin")
	if reservedPath != expectedPath {
		t.Fatalf("expected reserved path %q, got %q", expectedPath, reservedPath)
	}

	// Verify original sensitive file was untouched
	content, err := os.ReadFile(sensitiveFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "ORIGINAL_CONTENT" {
		t.Fatal("sensitive target file was overwritten through symlink conflict")
	}
}

// ============================================================================
// 2. PATH TRAVERSAL ATTEMPT TESTS
// ============================================================================

func TestTraversal_ValidateFilename_RejectsPathSeparatorsAndDots(t *testing.T) {
	traversalPayloads := []string{
		".",
		"..",
		"../secret.txt",
		"..\\secret.txt",
		"../../etc/passwd",
		"sub/../../etc/shadow",
		"dir/file.bin",
		"dir\\file.bin",
		"/absolute/path.bin",
		"C:\\Windows\\System32",
		"nested/dir/download.zip",
	}

	for _, payload := range traversalPayloads {
		t.Run(payload, func(t *testing.T) {
			err := ValidateFilename(payload)
			if err == nil {
				t.Fatalf("expected ValidateFilename to reject traversal payload %q", payload)
			}
		})
	}
}

func TestTraversal_ResolvePath_NormalizesWithoutEscaping(t *testing.T) {
	dir := t.TempDir()

	// Safe traversal within directory tree
	input := filepath.Join(dir, "a", "b", "..", "c", "download.bin")
	resolved, err := ResolvePath(input)
	if err != nil {
		t.Fatalf("ResolvePath failed: %v", err)
	}

	expected := filepath.Join(dir, "a", "c", "download.bin")
	if resolved != expected {
		t.Fatalf("expected resolved path %q, got %q", expected, resolved)
	}
}

// ============================================================================
// 3. CONFLICT RESOLUTION & CONCURRENCY TESTS
// ============================================================================

func TestConflict_SequentialNameGeneration(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "archive.tar.gz")

	// Create initial file
	if err := os.WriteFile(original, []byte("data-0"), 0600); err != nil {
		t.Fatal(err)
	}

	// Sequentially reserve 4 more conflicts
	expectedNames := []string{
		"archive.tar (1).gz",
		"archive.tar (2).gz",
		"archive.tar (3).gz",
		"archive.tar (4).gz",
	}

	for i, expectedName := range expectedNames {
		path, file, err := ReserveConflict(original, 0600)
		if err != nil {
			t.Fatalf("step %d: ReserveConflict failed: %v", i, err)
		}
		_ = file.Close()

		expectedFull := filepath.Join(dir, expectedName)
		if path != expectedFull {
			t.Fatalf("step %d: got %q, want %q", i, path, expectedFull)
		}
	}
}

func TestConflict_UnusualExtensionsAndDotfiles(t *testing.T) {
	dir := t.TempDir()

	testCases := []struct {
		name         string
		initialFile  string
		expectedNext string
	}{
		{
			name:         "no_extension",
			initialFile:  "README",
			expectedNext: "README (1)",
		},
		{
			name:         "hidden_dotfile",
			initialFile:  ".env",
			expectedNext: ".env (1)",
		},
		{
			name:         "multi_dot_release",
			initialFile:  "app.v2.1.0.release.iso",
			expectedNext: "app.v2.1.0.release (1).iso",
		},
		{
			name:         "trailing_dot",
			initialFile:  "filename.",
			expectedNext: "filename (1).",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.initialFile)
			if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}

			nextPath, file, err := ReserveConflict(path, 0600)
			if err != nil {
				t.Fatalf("ReserveConflict failed: %v", err)
			}
			defer file.Close()

			expected := filepath.Join(dir, tc.expectedNext)
			if nextPath != expected {
				t.Fatalf("expected %q, got %q", expected, nextPath)
			}
		})
	}
}

func TestConflict_HeavyConcurrentReservation(t *testing.T) {
	dir := t.TempDir()
	conflictingTarget := filepath.Join(dir, "concurrent_download.bin")

	// Pre-create original file
	if err := os.WriteFile(conflictingTarget, []byte("base-file"), 0600); err != nil {
		t.Fatal(err)
	}

	const numGoroutines = 25
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	reservedPaths := make([]string, numGoroutines)
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			path, file, err := ReserveConflict(conflictingTarget, 0600)
			if err != nil {
				errors[idx] = err
				return
			}
			defer file.Close()

			// Write unique identifier into the reserved file
			_, _ = file.Write([]byte(fmt.Sprintf("worker-%d", idx)))
			reservedPaths[idx] = path
		}()
	}

	wg.Wait()

	// Verify all goroutines succeeded without errors
	for i, err := range errors {
		if err != nil {
			t.Fatalf("worker %d returned error: %v", i, err)
		}
	}

	// Verify that every single reserved path is completely unique (no collisions)
	seen := make(map[string]bool)
	for i, path := range reservedPaths {
		if path == "" {
			t.Fatalf("worker %d received empty path", i)
		}
		if seen[path] {
			t.Fatalf("duplicate path collision detected: %q was reserved more than once", path)
		}
		seen[path] = true

		// Check file exists on disk and is a regular file
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat failed on reserved file %q: %v", path, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("reserved file %q is not a regular file", path)
		}
	}

	if len(seen) != numGoroutines {
		t.Fatalf("expected %d unique files, got %d", numGoroutines, len(seen))
	}
}

// ============================================================================
// 4. UNUSUAL FILENAMES & BOUNDARY TESTS
// ============================================================================

func TestUnusualFilenames_UnicodeAndEmojis(t *testing.T) {
	validUnicodeFilenames := []string{
		"🎬_Avengers_Secret_Wars_🍿_2026.mkv",
		"日本語のファイル名_ダウンロード.tar.gz",
		"ملف_تنزيل_عالي_الجودة.pdf",
		"डाउनलोड_फ़ाइल_2026.zip",
		"файл_для_тестирования.dat",
		"résumé_candidat_ingénieur.docx",
		"🔥_popular_track_🎵.flac",
		"🇨🇦_Canada_Tour_Guide.epub",
		"Greek_Alpha_Beta_Γειά_σου_Κόσμε.txt",
		"Chinese_中文_简体_测试文件.rar",
	}

	for _, filename := range validUnicodeFilenames {
		t.Run(filename, func(t *testing.T) {
			if err := ValidateFilename(filename); err != nil {
				t.Fatalf("expected valid Unicode filename %q, got error: %v", filename, err)
			}
		})
	}
}

func TestUnusualFilenames_SpacesAndPunctuation(t *testing.T) {
	validPunctuationFilenames := []string{
		"My Super Download 2026.iso",
		"  filename_with_leading_and_trailing_spaces.bin  ", // valid UTF-8, stripped or accepted
		"[Release] Hydra Download Manager - v1.0.0 (x86_64).tar.gz",
		"file-with-dashes_and_underscores.mp4",
		"symbols!@#$%^&()_+={}[];',~.bin",
		".hidden_config_file.yaml",
		"archive.tar.bz2",
		"test.final.v1.0.0.patch",
	}

	for _, filename := range validPunctuationFilenames {
		t.Run(filename, func(t *testing.T) {
			if err := ValidateFilename(filename); err != nil {
				t.Fatalf("expected valid filename with spaces/punctuation %q, got error: %v", filename, err)
			}
		})
	}
}

func TestUnusualFilenames_Boundary255Bytes(t *testing.T) {
	// Exactly 255 bytes (Linux limit for a filename component)
	exact255 := strings.Repeat("a", 251) + ".bin"
	if len([]byte(exact255)) != 255 {
		t.Fatalf("expected 255 bytes, got %d", len([]byte(exact255)))
	}

	if err := ValidateFilename(exact255); err != nil {
		t.Fatalf("expected 255-byte filename to be valid, got: %v", err)
	}

	// 256 bytes (exceeds Linux component limit)
	tooLong256 := strings.Repeat("a", 252) + ".bin"
	if len([]byte(tooLong256)) != 256 {
		t.Fatalf("expected 256 bytes, got %d", len([]byte(tooLong256)))
	}

	if err := ValidateFilename(tooLong256); err == nil {
		t.Fatal("expected 256-byte filename to be rejected by ValidateFilename")
	}
}

func TestUnusualFilenames_ControlCharactersAndNUL(t *testing.T) {
	invalidFilenames := []struct {
		name     string
		filename string
	}{
		{name: "nul_byte", filename: "bad\x00file.bin"},
		{name: "newline", filename: "bad\nfile.bin"},
		{name: "carriage_return", filename: "bad\rfile.bin"},
		{name: "tab", filename: "bad\tfile.bin"},
		{name: "escape_code", filename: "bad\x1bfile.bin"},
		{name: "bell", filename: "bad\x07file.bin"},
		{name: "all_whitespace_spaces", filename: "     "},
		{name: "empty_string", filename: ""},
		{name: "invalid_utf8", filename: string([]byte{0xff, 0xfe, 0xfd})},
	}

	for _, tc := range invalidFilenames {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateFilename(tc.filename); err == nil {
				t.Fatalf("expected ValidateFilename to reject %s (%q)", tc.name, tc.filename)
			}
		})
	}
}

func TestUnusualFilenames_TruncateFilename(t *testing.T) {
	dir := "/home/user/Downloads"

	// Short filename should remain unchanged
	short := filepath.Join(dir, "short.mkv")
	if got := TruncateFilename(short, 255); got != short {
		t.Fatalf("expected %q, got %q", short, got)
	}

	// Long filename truncated while preserving extension
	longBase := strings.Repeat("x", 260) + ".tar.gz"
	longPath := filepath.Join(dir, longBase)
	truncated := TruncateFilename(longPath, 100)

	baseResult := filepath.Base(truncated)
	if len(baseResult) > 100 {
		t.Fatalf("truncated filename length = %d > 100", len(baseResult))
	}
	if !strings.HasSuffix(baseResult, ".gz") {
		t.Fatalf("truncated filename lost extension: %q", baseResult)
	}
}
