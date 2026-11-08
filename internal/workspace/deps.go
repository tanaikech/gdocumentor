package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxAutoDepFiles = 15
const maxAutoDepTotalBytes = 250 * 1024

var (
	reGoImportSingle = regexp.MustCompile(`(?m)^\s*import\s+(?:[a-zA-Z0-9_.]+\s+)?"([^"]+)"`)
	reGoImportBlock  = regexp.MustCompile(`(?ms)import\s*\((.*?)\)`)
	reGoQuotedPkg    = regexp.MustCompile(`"([^"]+)"`)
	reJSImport       = regexp.MustCompile(`(?m)(?:import\s+.*?from\s+|require\s*\(\s*)['"](\.[^'"]+)['"]`)
	rePyFromImport   = regexp.MustCompile(`(?m)^\s*(?:from\s+([a-zA-Z0-9_.]+)\s+import|import\s+([a-zA-Z0-9_.]+))`)
	reCInclude       = regexp.MustCompile(`(?m)^\s*#include\s+"([^"]+)"`)
	reMdLink         = regexp.MustCompile(`\[[^\]]*\]\(([^)#?\s]+\.(?:md|markdown|txt|tex|rst))\)`)
	reTexInclude     = regexp.MustCompile(`\\(?:input|include)\{([^}]+)\}`)
)

// ResolveRelatedDependencies inspects the target files and automatically discovers related files
// (imported local packages/modules, sibling files in the same package, or linked/adjacent manuscript chapters)
// in parallel so Gemini can verify cross-file consistency and interactions.
func ResolveRelatedDependencies(workDir string, targetFiles []FileEntry, excludeAbsPaths map[string]bool) []FileEntry {
	seen := make(map[string]bool)
	for k, v := range excludeAbsPaths {
		if v {
			seen[k] = true
		}
	}

	var candidatePaths []string
	queueCandidate := func(absPath string) {
		clean := filepath.Clean(absPath)
		if seen[clean] {
			return
		}
		if !IsSupportedSourceOrTextFile(clean) {
			return
		}
		seen[clean] = true
		candidatePaths = append(candidatePaths, clean)
	}

	goModRoot, goModPath := findGoModuleRoot(workDir, targetFiles)

	for _, tf := range targetFiles {
		if tf.Path == "stdin" {
			continue
		}
		fileDir := filepath.Dir(tf.Path)
		ext := strings.ToLower(filepath.Ext(tf.Path))

		switch ext {
		case ".go":
			// 1. Other .go files in the same package directory (excluding _test.go unless target is a test)
			isTargetTest := strings.HasSuffix(tf.Path, "_test.go")
			if dirEntries, err := os.ReadDir(fileDir); err == nil {
				for _, de := range dirEntries {
					if de.IsDir() || !strings.HasSuffix(de.Name(), ".go") {
						continue
					}
					if !isTargetTest && strings.HasSuffix(de.Name(), "_test.go") {
						continue
					}
					queueCandidate(filepath.Join(fileDir, de.Name()))
				}
			}
			// 2. Imported internal packages within the same Go module
			if goModRoot != "" && goModPath != "" {
				pkgs := extractGoImports(tf.Content)
				for _, pkg := range pkgs {
					if after, ok := strings.CutPrefix(pkg, goModPath+"/"); ok {
						relPkgDir := after
						pkgAbsDir := filepath.Join(goModRoot, filepath.FromSlash(relPkgDir))
						if dirEntries, err := os.ReadDir(pkgAbsDir); err == nil {
							for _, de := range dirEntries {
								if !de.IsDir() && strings.HasSuffix(de.Name(), ".go") && !strings.HasSuffix(de.Name(), "_test.go") {
									queueCandidate(filepath.Join(pkgAbsDir, de.Name()))
								}
							}
						}
					}
				}
			}

		case ".js", ".ts", ".jsx", ".tsx", ".gs", ".mjs", ".cjs", ".html":
			matches := reJSImport.FindAllStringSubmatch(tf.Content, -1)
			for _, m := range matches {
				if len(m) > 1 {
					relImp := m[1]
					baseCandidate := filepath.Join(fileDir, filepath.FromSlash(relImp))
					candidates := []string{
						baseCandidate,
						baseCandidate + ".ts",
						baseCandidate + ".tsx",
						baseCandidate + ".js",
						baseCandidate + ".jsx",
						baseCandidate + ".gs",
						filepath.Join(baseCandidate, "index.ts"),
						filepath.Join(baseCandidate, "index.js"),
					}
					for _, c := range candidates {
						queueCandidate(c)
					}
				}
			}
			if dirEntries, err := os.ReadDir(fileDir); err == nil {
				for _, de := range dirEntries {
					if de.IsDir() {
						continue
					}
					nLower := strings.ToLower(de.Name())
					if nLower == "appsscript.json" || nLower == "package.json" || nLower == "tsconfig.json" ||
						strings.HasSuffix(nLower, ".gs") || strings.HasSuffix(nLower, ".js") ||
						strings.HasSuffix(nLower, ".ts") || strings.HasSuffix(nLower, ".html") {
						queueCandidate(filepath.Join(fileDir, de.Name()))
					}
				}
			}

		case ".py":
			matches := rePyFromImport.FindAllStringSubmatch(tf.Content, -1)
			for _, m := range matches {
				modName := m[1]
				if modName == "" {
					modName = m[2]
				}
				modName = strings.TrimLeft(modName, ".")
				if modName != "" {
					relPath := strings.ReplaceAll(modName, ".", string(filepath.Separator))
					queueCandidate(filepath.Join(fileDir, relPath+".py"))
					if workDir != "" {
						queueCandidate(filepath.Join(workDir, relPath+".py"))
					}
				}
			}

		case ".c", ".cpp", ".cc", ".h", ".hpp":
			matches := reCInclude.FindAllStringSubmatch(tf.Content, -1)
			for _, m := range matches {
				if len(m) > 1 {
					queueCandidate(filepath.Join(fileDir, filepath.FromSlash(m[1])))
				}
			}

		case ".md", ".markdown", ".txt", ".rst", ".tex":
			// 1. Explicitly linked markdown/text files
			for _, m := range reMdLink.FindAllStringSubmatch(tf.Content, -1) {
				if len(m) > 1 {
					queueCandidate(filepath.Join(fileDir, filepath.FromSlash(m[1])))
				}
			}
			// 2. LaTeX \input / \include
			for _, m := range reTexInclude.FindAllStringSubmatch(tf.Content, -1) {
				if len(m) > 1 {
					inc := m[1]
					if filepath.Ext(inc) == "" {
						inc += ".tex"
					}
					queueCandidate(filepath.Join(fileDir, filepath.FromSlash(inc)))
				}
			}
			// 3. Adjacent chapter/section files in the same directory
			if dirEntries, err := os.ReadDir(fileDir); err == nil {
				var names []string
				for _, de := range dirEntries {
					if !de.IsDir() && strings.ToLower(filepath.Ext(de.Name())) == ext {
						names = append(names, de.Name())
					}
				}
				sort.Strings(names)
				baseName := filepath.Base(tf.Path)
				for idx, n := range names {
					if n == baseName {
						if idx > 0 {
							queueCandidate(filepath.Join(fileDir, names[idx-1]))
						}
						if idx+1 < len(names) {
							queueCandidate(filepath.Join(fileDir, names[idx+1]))
						}
						break
					}
				}
			}
		}
	}

	if len(candidatePaths) == 0 {
		return nil
	}

	type indexedDep struct {
		idx   int
		entry *FileEntry
		size  int
	}
	resCh := make(chan indexedDep, len(candidatePaths))
	var wg sync.WaitGroup

	for idx, cp := range candidatePaths {
		wg.Add(1)
		go func(i int, clean string) {
			defer wg.Done()
			info, err := os.Stat(clean)
			if err != nil || info.IsDir() || info.Size() == 0 || info.Size() > 128*1024 {
				return
			}
			data, err := os.ReadFile(clean)
			if err != nil || !utf8.Valid(data) || isBinaryData(data) {
				return
			}
			resCh <- indexedDep{
				idx: i,
				entry: &FileEntry{
					Path:        clean,
					DisplayPath: makeDisplayPath(workDir, clean),
					Content:     normalizeNewlines(string(data)),
				},
				size: len(data),
			}
		}(idx, cp)
	}

	wg.Wait()
	close(resCh)

	var loaded []indexedDep
	for r := range resCh {
		if r.entry != nil {
			loaded = append(loaded, r)
		}
	}
	sort.Slice(loaded, func(i, j int) bool {
		return loaded[i].idx < loaded[j].idx
	})

	var discovered []FileEntry
	totalBytes := 0
	for _, item := range loaded {
		if len(discovered) >= maxAutoDepFiles || totalBytes+item.size > maxAutoDepTotalBytes {
			break
		}
		totalBytes += item.size
		discovered = append(discovered, *item.entry)
	}

	return discovered
}

func findGoModuleRoot(workDir string, targetFiles []FileEntry) (string, string) {
	startDir := workDir
	if len(targetFiles) > 0 && targetFiles[0].Path != "stdin" {
		startDir = filepath.Dir(targetFiles[0].Path)
	}
	curr := startDir
	for curr != "" && curr != filepath.Dir(curr) {
		gomodFile := filepath.Join(curr, "go.mod")
		if data, err := os.ReadFile(gomodFile); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				line = strings.TrimSpace(line)
				if after, ok := strings.CutPrefix(line, "module "); ok {
					modPath := strings.TrimSpace(after)
					return curr, modPath
				}
			}
		}
		curr = filepath.Dir(curr)
	}
	return "", ""
}

func extractGoImports(content string) []string {
	var pkgs []string
	seen := make(map[string]bool)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			pkgs = append(pkgs, p)
		}
	}
	for _, m := range reGoImportSingle.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, block := range reGoImportBlock.FindAllStringSubmatch(content, -1) {
		if len(block) > 1 {
			for _, q := range reGoQuotedPkg.FindAllStringSubmatch(block[1], -1) {
				if len(q) > 1 {
					add(q[1])
				}
			}
		}
	}
	return pkgs
}
