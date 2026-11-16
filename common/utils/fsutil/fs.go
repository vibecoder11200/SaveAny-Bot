package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/gabriel-vasile/mimetype"
)

// 删除文件夹内的所有文件和子目录, 但不删除文件夹本身
func RemoveAllInDir(dirPath string) error {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		if err := os.RemoveAll(entryPath); err != nil {
			return err
		}
	}
	return nil
}

func DetectFileExt(fp string) string {
	mt, err := mimetype.DetectFile(fp)
	if err != nil {
		return ""
	}
	return mt.Extension()
}

type File struct {
	*os.File
}

func (f *File) Remove() error {
	return os.Remove(f.Name())
}

func (f *File) CloseAndRemove() error {
	closeErr := f.Close()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	return errors.Join(closeErr, f.Remove())
}

func CreateFile(fp string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(fp), os.ModePerm); err != nil {
		return nil, err
	}
	file, err := os.Create(fp)
	if err != nil {
		return nil, err
	}
	return &File{File: file}, nil
}

// Sha256File returns the hex-encoded sha256 of the file at path, streamed
// with a 1 MiB buffer so large cache files never sit in memory twice.
func Sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.CopyBuffer(hasher, file, make([]byte, 1024*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// SafeBaseName reduces a remote-provided name to one safe filename component.
func SafeBaseName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = NormalizePathname(name)
	switch name {
	case "", ".", "..":
		return "file"
	}
	return ShiftReservedDeviceStem(name)
}

// winReservedDevices are the Windows filenames that open a device instead
// of creating a file: bare or followed by any extension.
var winReservedDevices = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ShiftReservedDeviceStem moves a Windows reserved device stem out of the
// reserved set by appending an underscore: Windows resolves such stems to
// the device itself even with an extension (CON.txt opens the console).
func ShiftReservedDeviceStem(name string) string {
	stem, _, _ := strings.Cut(name, ".")
	if winReservedDevices[strings.ToUpper(stem)] {
		return stem + "_" + name[len(stem):]
	}
	return name
}

func NormalizePathname(s string) string {
	specials := `\/:*?"<>|` + "\n\r\t"
	var builder strings.Builder
	for _, ch := range s {
		if strings.ContainsRune(specials, ch) || unicode.IsControl(ch) {
			builder.WriteRune('_')
		} else {
			builder.WriteRune(ch)
		}
	}

	result := strings.TrimRightFunc(builder.String(), func(r rune) bool {
		return r == '.' || r == '_' || unicode.IsSpace(r)
	})

	return result
}
