//go:build ignore

// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

// Build-time checks only; excluded from the streaming executable.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "notices":
		err = notices(os.Stdout)
	case len(os.Args) == 2 && os.Args[1] == "dupes":
		err = duplicates(".", os.Stdout)
	case len(os.Args) == 4 && os.Args[1] == "verify":
		var home, root string
		home, err = os.UserHomeDir()
		if err == nil {
			root, err = os.Getwd()
		}
		if err == nil {
			err = verifyArchive(os.Args[2], os.Args[3], []string{home, root, os.Getenv("GOMODCACHE")})
		}
	default:
		err = fmt.Errorf("usage: go run scripts/tools.go notices|dupes|verify ARCHIVE BINARY (from repository root)")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type module struct {
	Path, Version, Dir string
	Main               bool
	Replace            *module
}

func notices(out io.Writer) error {
	command := exec.Command("go", "list", "-m", "-json", "all")
	command.Stderr = os.Stderr
	data, err := command.Output()
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var packages []module
	for {
		var pkg module
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if !pkg.Main {
			packages = append(packages, pkg)
		}
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })
	var result bytes.Buffer
	result.WriteString("THIRD-PARTY NOTICES\n\nIncludes dependency and build-tool notices. Original license terms and copyright notices follow.\n")
	for _, pkg := range packages {
		directory := pkg.Dir
		if pkg.Replace != nil {
			directory = pkg.Replace.Dir
		}
		if directory == "" {
			return fmt.Errorf("Missing dependency source: %s; run go mod download", pkg.Path)
		}
		files, err := licenseFiles(directory)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("No license text found for %s %s", pkg.Path, pkg.Version)
		}
		fmt.Fprintf(&result, "\n=== %s %s ===\n", pkg.Path, pkg.Version)
		for _, name := range files {
			text, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				return err
			}
			// Match text-mode newline handling in the original notice generator.
			text = bytes.ReplaceAll(text, []byte("\r\n"), []byte("\n"))
			text = bytes.ReplaceAll(text, []byte("\r"), []byte("\n"))
			fmt.Fprintf(&result, "\n--- %s ---\n%s\n", filepath.ToSlash(name), text)
		}
	}
	_, err = out.Write(result.Bytes())
	return err
}

func licenseFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "target", ".build":
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		upper := strings.ToUpper(entry.Name())
		match := false
		for _, prefix := range []string{"LICENSE", "LICENCE", "NOTICE", "COPYING", "COPYRIGHT"} {
			match = match || strings.HasPrefix(upper, prefix)
		}
		for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(relative)), "/") {
			match = match || strings.EqualFold(part, "LICENSES") || strings.EqualFold(part, "LICENCES")
		}
		if match {
			files = append(files, relative)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func verifyArchive(path, binary string, prefixes []string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	expected := map[string]bool{binary: true, "README.md": true, "THIRD_PARTY_NOTICES.txt": true, "LICENSE.txt": true}
	for {
		member, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(member.Name, "./")
		if member.Typeflag == tar.TypeDir && (name == "" || name == ".") {
			continue
		}
		if member.Typeflag != tar.TypeReg || !expected[name] || member.Size <= 0 {
			return fmt.Errorf("Release rejected: missing or unexpected archive contents")
		}
		delete(expected, name)
		data, err := io.ReadAll(archive)
		if err != nil {
			return err
		}
		for _, prefix := range prefixes {
			if prefix != "" && bytes.Contains(data, []byte(prefix)) {
				return fmt.Errorf("Release rejected: local build path in %s", member.Name)
			}
		}
	}
	if len(expected) != 0 {
		return fmt.Errorf("Release rejected: missing or unexpected archive contents")
	}
	return nil
}

func duplicates(root string, out io.Writer) error {
	seen := make(map[string]string)
	clashes := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		positions := token.NewFileSet()
		file, err := parser.ParseFile(positions, path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			var body bytes.Buffer
			if err := printer.Fprint(&body, positions, function.Body); err != nil {
				return err
			}
			normalized := strings.Join(strings.Fields(body.String()), " ")
			if len(normalized) < 100 {
				continue
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			where := filepath.ToSlash(relative) + ":" + function.Name.Name
			if first, exists := seen[normalized]; exists {
				fmt.Fprintf(out, "  %s\n  %s\n\n", first, where)
				clashes = true
			} else {
				seen[normalized] = where
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if clashes {
		return fmt.Errorf("duplicated function bodies above. Share them, or say why not.")
	}
	_, err = fmt.Fprintln(out, "no duplicated function bodies")
	return err
}
