package distribution

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ManifestName        = "pack-manifest.json"
	PackManifestVersion = 1
	FileTypeDirectory   = "directory"
	FileTypeRegular     = "file"
	FileTypeSymlink     = "symlink"
)

type Format string

const (
	FormatTarGz Format = "tar.gz"
	FormatZip   Format = "zip"
)

func (format Format) Extension() string {
	switch format {
	case FormatTarGz:
		return ".tar.gz"
	case FormatZip:
		return ".zip"
	default:
		return ""
	}
}

type Metadata struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

type FileRecord struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Mode       uint32 `json:"mode"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
}

type Manifest struct {
	SchemaVersion int          `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Format        Format       `json:"format"`
	Files         []FileRecord `json:"files"`
	Size          int64        `json:"size,omitempty"`
	SHA256        string       `json:"sha256,omitempty"`
}

type packEntry struct {
	record     FileRecord
	sourcePath string
}

func WritePack(sourceRoot, outputPath string, format Format, metadata Metadata) (Manifest, error) {
	if err := validateMetadata(metadata); err != nil {
		return Manifest{}, err
	}
	if format.Extension() == "" {
		return Manifest{}, fmt.Errorf("unsupported pack format %q", format)
	}
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve pack source: %w", err)
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve pack output: %w", err)
	}
	if relative, err := filepath.Rel(sourceRoot, outputPath); err != nil {
		return Manifest{}, fmt.Errorf("compare pack paths: %w", err)
	} else if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Manifest{}, fmt.Errorf("pack output must be outside source tree")
	}
	entries, err := collectPackEntries(sourceRoot)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{SchemaVersion: PackManifestVersion, Metadata: metadata, Format: format, Files: make([]FileRecord, len(entries))}
	for i := range entries {
		manifest.Files[i] = entries[i].record
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("encode pack manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create pack output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".tmp-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create temporary pack: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if format == FormatTarGz {
		err = writeTarGzPack(temporary, entries, manifestJSON)
	} else {
		err = writeZipPack(temporary, entries, manifestJSON)
	}
	if err != nil {
		_ = temporary.Close()
		return Manifest{}, err
	}
	if err := temporary.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close pack: %w", err)
	}
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return Manifest{}, fmt.Errorf("replace existing pack: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return Manifest{}, fmt.Errorf("publish pack: %w", err)
	}
	archive, err := os.Open(outputPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open published pack: %w", err)
	}
	hash := sha256.New()
	manifest.Size, err = io.Copy(hash, archive)
	closeErr := archive.Close()
	if err != nil {
		return Manifest{}, fmt.Errorf("hash published pack: %w", err)
	}
	if closeErr != nil {
		return Manifest{}, fmt.Errorf("close published pack: %w", closeErr)
	}
	manifest.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return manifest, nil
}

func validateMetadata(metadata Metadata) error {
	fields := [][2]string{{"kind", metadata.Kind}, {"id", metadata.ID}, {"version", metadata.Version}, {"os", metadata.OS}, {"arch", metadata.Arch}}
	for _, field := range fields {
		if strings.TrimSpace(field[1]) == "" {
			return fmt.Errorf("pack metadata has no %s", field[0])
		}
		if strings.ContainsAny(field[1], "/\\\r\n\t") {
			return fmt.Errorf("pack metadata %s contains unsafe characters", field[0])
		}
	}
	return nil
}

func collectPackEntries(sourceRoot string) ([]packEntry, error) {
	entries := make([]packEntry, 0)
	err := filepath.WalkDir(sourceRoot, func(sourcePath string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if sourcePath == sourceRoot {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		archivePath := filepath.ToSlash(relative)
		if archivePath == ManifestName {
			return fmt.Errorf("pack source contains reserved path %q", ManifestName)
		}
		info, err := dirEntry.Info()
		if err != nil {
			return fmt.Errorf("inspect pack entry %q: %w", archivePath, err)
		}
		record := FileRecord{Path: archivePath, Mode: normalizedMode(info)}
		switch {
		case info.Mode().IsDir():
			record.Type = FileTypeDirectory
		case info.Mode()&os.ModeSymlink != 0:
			record.Type = FileTypeSymlink
			record.LinkTarget, err = os.Readlink(sourcePath)
			if err != nil {
				return fmt.Errorf("read pack symlink %q: %w", archivePath, err)
			}
			if err := validateLinkTarget(archivePath, record.LinkTarget); err != nil {
				return err
			}
			hash := sha256.Sum256([]byte(record.LinkTarget))
			record.Size = int64(len(record.LinkTarget))
			record.SHA256 = hex.EncodeToString(hash[:])
		case info.Mode().IsRegular():
			record.Type = FileTypeRegular
			file, err := os.Open(sourcePath)
			if err != nil {
				return fmt.Errorf("open pack entry %q: %w", archivePath, err)
			}
			hash := sha256.New()
			record.Size, err = io.Copy(hash, file)
			closeErr := file.Close()
			if err != nil {
				return fmt.Errorf("hash pack entry %q: %w", archivePath, err)
			}
			if closeErr != nil {
				return fmt.Errorf("close pack entry %q: %w", archivePath, closeErr)
			}
			record.SHA256 = hex.EncodeToString(hash.Sum(nil))
		default:
			return fmt.Errorf("pack entry %q has unsupported file type", archivePath)
		}
		entries = append(entries, packEntry{record: record, sourcePath: sourcePath})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect pack entries: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].record.Path < entries[j].record.Path })
	return entries, nil
}

func normalizedMode(info os.FileInfo) uint32 {
	if info.IsDir() {
		return 0o755
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0o777
	}
	if info.Mode().Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func validateLinkTarget(archivePath, target string) error {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") {
		return fmt.Errorf("pack symlink %q has unsafe target %q", archivePath, target)
	}
	resolved := path.Clean(path.Join(path.Dir(archivePath), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("pack symlink %q escapes archive root", archivePath)
	}
	return nil
}

func writeTarGzPack(output io.Writer, entries []packEntry, manifestJSON []byte) error {
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip pack: %w", err)
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.record.Path, Mode: int64(entry.record.Mode), ModTime: time.Unix(0, 0), AccessTime: time.Unix(0, 0), ChangeTime: time.Unix(0, 0), Uid: 0, Gid: 0}
		switch entry.record.Type {
		case FileTypeDirectory:
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		case FileTypeSymlink:
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.record.LinkTarget
		case FileTypeRegular:
			header.Typeflag = tar.TypeReg
			header.Size = entry.record.Size
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar header %q: %w", entry.record.Path, err)
		}
		if entry.record.Type == FileTypeRegular {
			if err := copyPackFile(tarWriter, entry); err != nil {
				return err
			}
		}
	}
	manifestHeader := &tar.Header{Name: ManifestName, Mode: 0o644, Size: int64(len(manifestJSON)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), AccessTime: time.Unix(0, 0), ChangeTime: time.Unix(0, 0)}
	if err := tarWriter.WriteHeader(manifestHeader); err != nil {
		return fmt.Errorf("write tar manifest header: %w", err)
	}
	if _, err := tarWriter.Write(manifestJSON); err != nil {
		return fmt.Errorf("write tar manifest: %w", err)
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close tar pack: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("close gzip pack: %w", err)
	}
	return nil
}

func writeZipPack(output io.Writer, entries []packEntry, manifestJSON []byte) error {
	zipWriter := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.record.Path, Method: zip.Deflate}
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		mode := os.FileMode(entry.record.Mode)
		if entry.record.Type == FileTypeDirectory {
			header.Name += "/"
			mode |= os.ModeDir
		} else if entry.record.Type == FileTypeSymlink {
			mode |= os.ModeSymlink
		}
		header.SetMode(mode)
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("write zip header %q: %w", entry.record.Path, err)
		}
		if entry.record.Type == FileTypeRegular {
			if err := copyPackFile(writer, entry); err != nil {
				return err
			}
		} else if entry.record.Type == FileTypeSymlink {
			if _, err := io.WriteString(writer, entry.record.LinkTarget); err != nil {
				return fmt.Errorf("write zip symlink %q: %w", entry.record.Path, err)
			}
		}
	}
	manifestHeader := &zip.FileHeader{Name: ManifestName, Method: zip.Deflate}
	manifestHeader.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
	manifestHeader.SetMode(0o644)
	manifestWriter, err := zipWriter.CreateHeader(manifestHeader)
	if err != nil {
		return fmt.Errorf("write zip manifest header: %w", err)
	}
	if _, err := manifestWriter.Write(manifestJSON); err != nil {
		return fmt.Errorf("write zip manifest: %w", err)
	}
	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("close zip pack: %w", err)
	}
	return nil
}

func copyPackFile(output io.Writer, entry packEntry) error {
	file, err := os.Open(entry.sourcePath)
	if err != nil {
		return fmt.Errorf("open pack entry %q: %w", entry.record.Path, err)
	}
	_, copyErr := io.Copy(output, file)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write pack entry %q: %w", entry.record.Path, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close pack entry %q: %w", entry.record.Path, closeErr)
	}
	return nil
}
