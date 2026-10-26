package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// UnpackFunc extracts the installable binary from a downloaded artifact.
//
// The artifact is only ever read from; the binary must be written to w, which
// Updater has staged next to the target and will chmod and rename into place.
// rel is passed along so a hook can branch on the version, the platform or any
// field its own Source put there.
//
// Nothing about the format is assumed: a tarball, a zip, a signed envelope or a
// single-file installer all fit. update.TarGz and update.Zip cover the two
// shapes Go releases normally ship in.
type UnpackFunc func(rel Release, artifact io.Reader, w io.Writer) error

// TarGz returns an UnpackFunc that copies member out of a .tar.gz/.tgz
// artifact. member is matched against the entry name and, failing an exact
// match, against its base name, so "app" also finds "app_1.2.3/app".
func TarGz(member string) UnpackFunc {
	return func(rel Release, artifact io.Reader, w io.Writer) error {
		gz, err := gzip.NewReader(artifact)
		if err != nil {
			return fmt.Errorf("update: %s is not gzip compressed: %w", rel.URL, err)
		}
		defer gz.Close()

		reader := tar.NewReader(gz)
		var entries []string
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("update: reading %s: %w", rel.URL, err)
			}
			if header.Typeflag != tar.TypeReg {
				continue
			}
			entries = append(entries, header.Name)
			if !memberMatch(header.Name, member) {
				continue
			}
			if _, err := io.Copy(w, reader); err != nil {
				return fmt.Errorf("update: extracting %s from %s: %w", header.Name, rel.URL, err)
			}
			return nil
		}
		return missingMember(rel, member, entries)
	}
}

// Zip returns an UnpackFunc that copies member out of a zip artifact, with the
// same name matching as TarGz. The artifact must be an *os.File, which is what
// Updater always hands over; zips are read through a random-access index.
func Zip(member string) UnpackFunc {
	return func(rel Release, artifact io.Reader, w io.Writer) error {
		file, ok := artifact.(*os.File)
		if !ok {
			return errors.New("update: update.Zip needs the artifact as an *os.File")
		}
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("update: sizing %s: %w", rel.URL, err)
		}
		reader, err := zip.NewReader(file, info.Size())
		if err != nil {
			return fmt.Errorf("update: %s is not a zip archive: %w", rel.URL, err)
		}

		entries := make([]string, 0, len(reader.File))
		for _, entry := range reader.File {
			if entry.FileInfo().IsDir() {
				continue
			}
			entries = append(entries, entry.Name)
			if !memberMatch(entry.Name, member) {
				continue
			}
			src, err := entry.Open()
			if err != nil {
				return fmt.Errorf("update: opening %s in %s: %w", entry.Name, rel.URL, err)
			}
			defer src.Close()
			if _, err := io.Copy(w, src); err != nil {
				return fmt.Errorf("update: extracting %s from %s: %w", entry.Name, rel.URL, err)
			}
			return nil
		}
		return missingMember(rel, member, entries)
	}
}

// memberMatch prefers the full path, then the base name: releases are usually
// packed as "app_1.2.3_linux_amd64/app" but a flat "app_linux_amd64" is just as
// common.
func memberMatch(name, member string) bool {
	if member == "" {
		return false
	}
	if name == member {
		return true
	}
	return path.Base(name) == member
}

// missingMember reports what was asked for and what the archive actually holds,
// which turns the most common packaging mistake into a one line fix.
func missingMember(rel Release, member string, entries []string) error {
	if member == "" {
		return fmt.Errorf("update: %s contains %s, set a member name", rel.URL, entryList(entries))
	}
	return fmt.Errorf("update: %s has no entry %q, it contains %s", rel.URL, member, entryList(entries))
}

func entryList(entries []string) string {
	if len(entries) == 0 {
		return "no regular files"
	}
	const shown = 8
	names := entries
	suffix := ""
	if len(names) > shown {
		names = names[:shown]
		suffix = fmt.Sprintf(" and %d more", len(entries)-shown)
	}
	return strings.Join(names, ", ") + suffix
}
