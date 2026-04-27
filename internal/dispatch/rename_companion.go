package dispatch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/output"
)

// companionExtsForLang returns the template / view / DSL extensions
// associated with a source language. Rename does not parse these
// (their syntax is heterogeneous — ERB embeds Ruby in HTML, jbuilder
// is method-chained DSL, etc.), so we only scan them for word-bounded
// mentions and warn — not rewrite.
func companionExtsForLang(srcExt string) []string {
	switch strings.ToLower(srcExt) {
	case ".rb":
		return []string{".erb", ".haml", ".slim", ".rabl", ".jbuilder", ".builder"}
	case ".php":
		return []string{".twig", ".vue"}
	}
	return nil
}

type companionMention struct {
	file  string
	count int
}

// companionFileMentions scans companion / template files (per
// companionExtsForLang) for word-bounded mentions of oldName.
// Files already targeted by the rename pass (in fileSpans) are
// skipped — the existing code-mention warning covers those.
//
// Uses the trigram index when available (matches the access pattern
// of `edr files`) and falls back to a filesystem walk. Returns the
// total mention count and per-file breakdown for the warning.
func companionFileMentions(root, edrDir, srcFile, oldName string, fileSpans map[string][]span) (int, []companionMention) {
	exts := companionExtsForLang(filepath.Ext(srcFile))
	if len(exts) == 0 || oldName == "" {
		return 0, nil
	}
	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		extSet[e] = true
	}

	var candidates []string
	if h, _ := idx.ReadHeader(edrDir); h != nil && len(oldName) >= 3 {
		tris := idx.QueryTrigrams(strings.ToLower(oldName))
		if cands, ok := idx.Query(edrDir, tris); ok {
			for _, rel := range cands {
				if extSet[strings.ToLower(filepath.Ext(rel))] {
					candidates = append(candidates, filepath.Join(root, rel))
				}
			}
		}
	}
	if candidates == nil {
		// Fallback walk — no usable index, or oldName too short for trigrams.
		skipDirs := map[string]bool{".git": true, ".edr": true, "vendor": true, ".bundle": true, "node_modules": true, "tmp": true, "log": true, "coverage": true}
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				if skipDirs[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if extSet[strings.ToLower(filepath.Ext(path))] {
				candidates = append(candidates, path)
			}
			return nil
		})
	}

	total := 0
	var mentions []companionMention
	for _, abs := range candidates {
		if _, alreadyEdited := fileSpans[abs]; alreadyEdited {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		spans := findIdentOccurrences(data, 0, uint32(len(data)), oldName)
		if len(spans) == 0 {
			continue
		}
		total += len(spans)
		mentions = append(mentions, companionMention{file: output.Rel(abs), count: len(spans)})
	}
	return total, mentions
}
