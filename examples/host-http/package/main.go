// Package main creates a reproducible native bundle for the HTTP fixture.
// This is example-specific build code, not a Provision deployment operation.
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func main() {
	input := flag.String("input", "", "built Linux executable")
	output := flag.String("output", "", "bundle destination")
	flag.Parse()
	if *input == "" || *output == "" || flag.NArg() != 0 {
		fail("usage: package --input EXECUTABLE --output BUNDLE.tar.gz")
	}
	if err := pack(*input, *output); err != nil {
		fail("package fixture: %v", err)
	}
}

func pack(input, output string) error {
	source, err := os.Open(input)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", input)
	}
	temp, err := os.CreateTemp(filepath.Dir(output), ".hello-bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	gzipWriter := gzip.NewWriter(temp)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: "hello", Mode: 0755, Size: info.Size(), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
	if err := tarWriter.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.Copy(tarWriter, source); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), output)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
