package distribution

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxPackEntries        = 250_000
	maxPackExtractedBytes = int64(8 << 30)
	maxPackManifestBytes  = int64(8 << 20)
)

func ExtractPack(archivePath string, format Format, destination string, expected Metadata) (Manifest, error) {
	manifest, err := readPackManifest(archivePath, format)
	if err != nil {
		return Manifest{}, err
	}
	records, err := validatePackManifest(manifest, format, expected)
	if err != nil {
		return Manifest{}, err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve pack destination: %w", err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create pack destination: %w", err)
	}
	if format == FormatTarGz {
		err = extractTarGzPack(archivePath, destination, records)
	} else if format == FormatZip {
		err = extractZipPack(archivePath, destination, records)
	} else {
		err = fmt.Errorf("unsupported pack format %q", format)
	}
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func readPackManifest(archivePath string, format Format) (Manifest, error) {
	var data []byte
	switch format {
	case FormatTarGz:
		archive, err := os.Open(archivePath)
		if err != nil {
			return Manifest{}, fmt.Errorf("open tar pack: %w", err)
		}
		gzipReader, err := gzip.NewReader(archive)
		if err != nil {
			_ = archive.Close()
			return Manifest{}, fmt.Errorf("open gzip pack: %w", err)
		}
		tarReader := tar.NewReader(gzipReader)
		found := false
		for {
			header, err := tarReader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = gzipReader.Close()
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("read tar pack: %w", err)
			}
			if strings.TrimSuffix(header.Name, "/") != ManifestName {
				continue
			}
			if found || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxPackManifestBytes {
				_ = gzipReader.Close()
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("tar pack has invalid embedded manifest")
			}
			data = make([]byte, header.Size)
			if _, err := io.ReadFull(tarReader, data); err != nil {
				_ = gzipReader.Close()
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("read tar pack manifest: %w", err)
			}
			found = true
		}
		if err := gzipReader.Close(); err != nil {
			_ = archive.Close()
			return Manifest{}, fmt.Errorf("close gzip pack: %w", err)
		}
		if err := archive.Close(); err != nil {
			return Manifest{}, fmt.Errorf("close tar pack: %w", err)
		}
	case FormatZip:
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			return Manifest{}, fmt.Errorf("open zip pack: %w", err)
		}
		found := false
		for _, file := range archive.File {
			if strings.TrimSuffix(file.Name, "/") != ManifestName {
				continue
			}
			if found || file.UncompressedSize64 > uint64(maxPackManifestBytes) || !file.Mode().IsRegular() {
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("zip pack has invalid embedded manifest")
			}
			reader, err := file.Open()
			if err != nil {
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("open zip pack manifest: %w", err)
			}
			data, err = io.ReadAll(io.LimitReader(reader, maxPackManifestBytes+1))
			closeErr := reader.Close()
			if err != nil {
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("read zip pack manifest: %w", err)
			}
			if closeErr != nil {
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("close zip pack manifest: %w", closeErr)
			}
			if int64(len(data)) > maxPackManifestBytes {
				_ = archive.Close()
				return Manifest{}, fmt.Errorf("zip pack manifest exceeds size limit")
			}
			found = true
		}
		if err := archive.Close(); err != nil {
			return Manifest{}, fmt.Errorf("close zip pack: %w", err)
		}
	default:
		return Manifest{}, fmt.Errorf("unsupported pack format %q", format)
	}
	if data == nil {
		return Manifest{}, fmt.Errorf("pack has no embedded manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode pack manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, fmt.Errorf("decode pack manifest: trailing JSON data")
	}
	return manifest, nil
}

func validatePackManifest(manifest Manifest, format Format, expected Metadata) (map[string]FileRecord, error) {
	if manifest.SchemaVersion != PackManifestVersion {
		return nil, fmt.Errorf("unsupported pack manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.Metadata != expected {
		return nil, fmt.Errorf("pack metadata does not match release component")
	}
	if manifest.Format != format {
		return nil, fmt.Errorf("pack format does not match release component")
	}
	if len(manifest.Files) > maxPackEntries {
		return nil, fmt.Errorf("pack exceeds %d entry limit", maxPackEntries)
	}
	records := make(map[string]FileRecord, len(manifest.Files))
	var totalSize int64
	for _, record := range manifest.Files {
		cleaned, err := cleanPackPath(record.Path)
		if err != nil || cleaned == ManifestName {
			return nil, fmt.Errorf("pack manifest has unsafe path %q", record.Path)
		}
		if _, exists := records[cleaned]; exists {
			return nil, fmt.Errorf("pack manifest repeats path %q", cleaned)
		}
		record.Path = cleaned
		switch record.Type {
		case FileTypeDirectory:
			if record.Mode != 0o755 || record.Size != 0 || record.SHA256 != "" || record.LinkTarget != "" {
				return nil, fmt.Errorf("pack directory %q has invalid metadata", cleaned)
			}
		case FileTypeRegular:
			if record.Mode != 0o644 && record.Mode != 0o755 {
				return nil, fmt.Errorf("pack file %q has invalid mode", cleaned)
			}
			if record.Size < 0 || record.Size > maxPackExtractedBytes-totalSize {
				return nil, fmt.Errorf("pack exceeds %d extracted byte limit", maxPackExtractedBytes)
			}
			totalSize += record.Size
			digest, err := hex.DecodeString(record.SHA256)
			if err != nil || len(digest) != 32 || record.LinkTarget != "" {
				return nil, fmt.Errorf("pack file %q has invalid digest metadata", cleaned)
			}
		case FileTypeSymlink:
			if record.Mode != 0o777 || record.Size != int64(len(record.LinkTarget)) {
				return nil, fmt.Errorf("pack symlink %q has invalid metadata", cleaned)
			}
			if err := validateLinkTarget(cleaned, record.LinkTarget); err != nil {
				return nil, err
			}
			digest := sha256.Sum256([]byte(record.LinkTarget))
			if record.SHA256 != hex.EncodeToString(digest[:]) {
				return nil, fmt.Errorf("pack symlink %q has invalid digest", cleaned)
			}
		default:
			return nil, fmt.Errorf("pack path %q has unsupported type %q", cleaned, record.Type)
		}
		records[cleaned] = record
	}
	return records, nil
}

func extractTarGzPack(archivePath, destination string, records map[string]FileRecord) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar pack: %w", err)
	}
	defer archive.Close()
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open gzip pack: %w", err)
	}
	defer gzipReader.Close()
	seen := make(map[string]bool, len(records))
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar pack: %w", err)
		}
		entryPath := strings.TrimSuffix(header.Name, "/")
		if entryPath == ManifestName {
			continue
		}
		record, err := packRecordForEntry(entryPath, records, seen)
		if err != nil {
			return err
		}
		switch record.Type {
		case FileTypeDirectory:
			if header.Typeflag != tar.TypeDir {
				return fmt.Errorf("tar entry %q type does not match manifest", record.Path)
			}
			if err := extractPackDirectory(destination, record); err != nil {
				return err
			}
		case FileTypeRegular:
			if header.Typeflag != tar.TypeReg || header.Size != record.Size {
				return fmt.Errorf("tar entry %q size or type does not match manifest", record.Path)
			}
			if err := extractPackFile(destination, record, tarReader); err != nil {
				return err
			}
		case FileTypeSymlink:
			if header.Typeflag != tar.TypeSymlink || header.Linkname != record.LinkTarget {
				return fmt.Errorf("tar symlink %q does not match manifest", record.Path)
			}
			if err := extractPackSymlink(destination, record); err != nil {
				return err
			}
		}
	}
	return ensureCompletePack(records, seen)
}

func extractZipPack(archivePath, destination string, records map[string]FileRecord) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip pack: %w", err)
	}
	defer archive.Close()
	seen := make(map[string]bool, len(records))
	for _, file := range archive.File {
		entryPath := strings.TrimSuffix(file.Name, "/")
		if entryPath == ManifestName {
			continue
		}
		record, err := packRecordForEntry(entryPath, records, seen)
		if err != nil {
			return err
		}
		switch record.Type {
		case FileTypeDirectory:
			if !file.FileInfo().IsDir() {
				return fmt.Errorf("zip entry %q type does not match manifest", record.Path)
			}
			if err := extractPackDirectory(destination, record); err != nil {
				return err
			}
		case FileTypeRegular:
			if !file.Mode().IsRegular() || file.UncompressedSize64 != uint64(record.Size) {
				return fmt.Errorf("zip entry %q size or type does not match manifest", record.Path)
			}
			reader, err := file.Open()
			if err != nil {
				return fmt.Errorf("open zip entry %q: %w", record.Path, err)
			}
			extractErr := extractPackFile(destination, record, reader)
			closeErr := reader.Close()
			if extractErr != nil {
				return extractErr
			}
			if closeErr != nil {
				return fmt.Errorf("close zip entry %q: %w", record.Path, closeErr)
			}
		case FileTypeSymlink:
			if file.Mode()&os.ModeSymlink == 0 || file.UncompressedSize64 != uint64(record.Size) {
				return fmt.Errorf("zip symlink %q does not match manifest", record.Path)
			}
			reader, err := file.Open()
			if err != nil {
				return fmt.Errorf("open zip symlink %q: %w", record.Path, err)
			}
			target, err := io.ReadAll(io.LimitReader(reader, record.Size+1))
			closeErr := reader.Close()
			if err != nil || closeErr != nil || string(target) != record.LinkTarget {
				return fmt.Errorf("zip symlink %q target does not match manifest", record.Path)
			}
			if err := extractPackSymlink(destination, record); err != nil {
				return err
			}
		}
	}
	return ensureCompletePack(records, seen)
}

func packRecordForEntry(entryPath string, records map[string]FileRecord, seen map[string]bool) (FileRecord, error) {
	cleaned, err := cleanPackPath(entryPath)
	if err != nil {
		return FileRecord{}, err
	}
	record, ok := records[cleaned]
	if !ok {
		return FileRecord{}, fmt.Errorf("archive contains unlisted entry %q", cleaned)
	}
	if seen[cleaned] {
		return FileRecord{}, fmt.Errorf("archive repeats entry %q", cleaned)
	}
	seen[cleaned] = true
	return record, nil
}

func cleanPackPath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
		return "", fmt.Errorf("unsafe pack path %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned != name || path.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe pack path %q", name)
	}
	return cleaned, nil
}

func safePackTarget(destination, archivePath string) (string, error) {
	target := filepath.Join(destination, filepath.FromSlash(archivePath))
	relative, err := filepath.Rel(destination, target)
	if err != nil {
		return "", fmt.Errorf("resolve pack path %q: %w", archivePath, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("pack path %q escapes destination", archivePath)
	}
	return target, nil
}

func ensureSafePackParent(destination, archivePath string) error {
	parts := strings.Split(path.Dir(archivePath), "/")
	current := destination
	for _, part := range parts {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return fmt.Errorf("create pack directory %q: %w", current, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect pack directory %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("pack path %q traverses symlink parent", archivePath)
		}
		if !info.IsDir() {
			return fmt.Errorf("pack path %q has non-directory parent", archivePath)
		}
	}
	return nil
}

func extractPackDirectory(destination string, record FileRecord) error {
	if err := ensureSafePackParent(destination, record.Path); err != nil {
		return err
	}
	target, err := safePackTarget(destination, record.Path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return os.Mkdir(target, os.FileMode(record.Mode))
	}
	if err != nil {
		return fmt.Errorf("inspect pack directory %q: %w", record.Path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("pack directory %q replaces symlink", record.Path)
	}
	if !info.IsDir() {
		return fmt.Errorf("pack directory %q replaces non-directory", record.Path)
	}
	return os.Chmod(target, os.FileMode(record.Mode))
}

func extractPackFile(destination string, record FileRecord, input io.Reader) error {
	if err := ensureSafePackParent(destination, record.Path); err != nil {
		return err
	}
	target, err := safePackTarget(destination, record.Path)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(record.Mode))
	if err != nil {
		return fmt.Errorf("create pack file %q: %w", record.Path, err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, record.Size+1))
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("extract pack file %q: %w", record.Path, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close pack file %q: %w", record.Path, closeErr)
	}
	if written != record.Size {
		return fmt.Errorf("pack file %q size does not match manifest", record.Path)
	}
	if hex.EncodeToString(hash.Sum(nil)) != record.SHA256 {
		return fmt.Errorf("pack file %q hash does not match manifest", record.Path)
	}
	if err := os.Chmod(target, os.FileMode(record.Mode)); err != nil {
		return fmt.Errorf("set pack file mode %q: %w", record.Path, err)
	}
	return nil
}

func extractPackSymlink(destination string, record FileRecord) error {
	if err := ensureSafePackParent(destination, record.Path); err != nil {
		return err
	}
	target, err := safePackTarget(destination, record.Path)
	if err != nil {
		return err
	}
	if err := os.Symlink(record.LinkTarget, target); err != nil {
		return fmt.Errorf("create pack symlink %q: %w", record.Path, err)
	}
	return nil
}

func ensureCompletePack(records map[string]FileRecord, seen map[string]bool) error {
	if len(seen) == len(records) {
		return nil
	}
	for entryPath := range records {
		if !seen[entryPath] {
			return fmt.Errorf("archive lacks manifest entry %q", entryPath)
		}
	}
	panic("pack entry counts differ without missing record")
}
