package main

import (
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/andybalholm/brotli"
)

var textExts = map[string]bool{
	".css":         true,
	".html":        true,
	".js":          true,
	".json":        true,
	".map":         true,
	".svg":         true,
	".txt":         true,
	".webmanifest": true,
	".xml":         true,
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: precompress <assets-dir>")
		os.Exit(2)
	}
	root := os.Args[1]
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !textExts[ext] {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < 1024 {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeBrotli(path+".br", data); err != nil {
			return err
		}
		if err := writeGzip(path+".gz", data); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "precompress:", err)
		os.Exit(1)
	}
	fmt.Printf("预压缩完成：%d 个前端资源（.br + .gz）\n", count)
}

func writeBrotli(name string, data []byte) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	w := brotli.NewWriterLevel(f, 5)
	if _, err = w.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = w.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeGzip(name string, data []byte) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	w, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		_ = f.Close()
		return err
	}
	if _, err = w.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = w.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
