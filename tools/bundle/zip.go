package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// zipBundle writes the .app directory to a zip (the bundle directory at the
// root, Unix modes kept so the executable stays executable), atomically.
func zipBundle(app, out string) (err error) {
	tmp := out + ".partial"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}

	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	zw := zip.NewWriter(f)
	base := filepath.Dir(app)

	err = filepath.WalkDir(app, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}

		return addToZip(zw, base, path, d)
	})
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}

	if err = zw.Close(); err != nil {
		return fmt.Errorf("zip: %w", err)
	}

	if err = f.Close(); err != nil {
		return fmt.Errorf("zip: %w", err)
	}

	return os.Rename(tmp, out)
}

func addToZip(zw *zip.Writer, base, path string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}

	rel, err := filepath.Rel(base, path)
	if err != nil {
		return err
	}

	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	hdr.Name = filepath.ToSlash(rel)

	if d.IsDir() {
		hdr.Name += "/"
		_, err = zw.CreateHeader(hdr)

		return err
	}

	hdr.Method = zip.Deflate

	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}

	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	_, err = io.Copy(w, src)

	return err
}
