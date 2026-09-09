//go:build ignore

// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLicenseFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"LICENSE", "sub/NOTICE.txt", "LICENSES/MIT.txt", "target/LICENSE", ".git/LICENSE", "source.go"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := licenseFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LICENSE", filepath.FromSlash("LICENSES/MIT.txt"), filepath.FromSlash("sub/NOTICE.txt")}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("got %v, want %v", files, want)
	}
}

func TestVerifyArchive(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "empty", "extra", "duplicate", "path", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			var buffer bytes.Buffer
			compressed := gzip.NewWriter(&buffer)
			archive := tar.NewWriter(compressed)
			names := []string{"wagastrim", "README.md", "LICENSE.txt", "THIRD_PARTY_NOTICES.txt"}
			switch scenario {
			case "missing":
				names = names[:3]
			case "extra":
				names = append(names, "tools")
			case "duplicate":
				names = append(names, "wagastrim")
			}
			for _, name := range names {
				data := "contents"
				kind := byte(tar.TypeReg)
				if name == "wagastrim" {
					switch scenario {
					case "empty":
						data = ""
					case "path":
						data = "local /test/private/build path"
					case "symlink":
						kind, data = tar.TypeSymlink, ""
					}
				}
				if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: kind}); err != nil {
					t.Fatal(err)
				}
				if _, err := archive.Write([]byte(data)); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "release.tar.gz")
			if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifyArchive(path, "wagastrim", []string{"", "/test/private"})
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}

func TestDuplicatesAcrossBuildTags(t *testing.T) {
	root := t.TempDir()
	body := "{ println(`literal } // not a comment`); println(\"long enough to exercise duplicate detection across files and build constraints\") }"
	for _, name := range []string{"first.go", "second_test.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture\nfunc first() "+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := duplicates(root, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "second.go"), []byte("//go:build windows\n\npackage fixture\nfunc second() "+body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := duplicates(root, &bytes.Buffer{}); err == nil {
		t.Fatal("missed duplicate behind build constraint")
	}
}
