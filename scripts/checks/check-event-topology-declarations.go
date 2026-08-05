package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := "modules"
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := findTopologyDeclarations(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, finding := range findings {
		fmt.Println(finding)
	}
}

func findTopologyDeclarations(root string) ([]string, error) {
	files := token.NewFileSet()
	var findings []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			declaration, ok := node.(*ast.GenDecl)
			if !ok || (declaration.Tok != token.CONST && declaration.Tok != token.VAR) {
				return true
			}
			for _, spec := range declaration.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if isTopologyName(name.Name) {
						position := files.Position(name.Pos())
						findings = append(findings, fmt.Sprintf("%s:%d:%s", path, position.Line, name.Name))
					}
				}
			}
			return false
		})
		return nil
	})
	return findings, err
}

func isTopologyName(name string) bool {
	return name == "Topic" ||
		name == "Stream" ||
		name == "SubjectPrefix" ||
		strings.HasSuffix(name, "Topic") ||
		strings.HasSuffix(name, "Stream") ||
		strings.HasSuffix(name, "SubjectPrefix")
}
