package index

import (
	"fmt"
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// blockNodeTypes are AST node types that represent control flow blocks.
// When collapsing, these get their body replaced with "..."
var blockNodeTypes = map[string]bool{
	// Shared
	"if_statement": true, "for_statement": true, "while_statement": true,
	"try_statement": true, "switch_statement": true,
	// Go
	"if_expression": true, "for_expression": true, "expression_switch_statement": true,
	"type_switch_statement": true, "select_statement": true,
	// Python
	"with_statement": true, "match_statement": true, "elif_clause": true,
	"else_clause": true, "except_clause": true, "finally_clause": true,
	// JS/TS
	"for_in_statement": true, "switch_case": true, "catch_clause": true,
	// Rust
	"if_expression_rust": true, "match_expression": true, "loop_expression": true,
	// Ruby
	"if": true, "unless": true, "while": true, "for": true, "begin": true, "case": true,
	// C/C++ definitions — collapsed at depth=1 (--sig) but visible at depth=2 (--skeleton)
	"function_definition": true, "struct_specifier": true, "enum_specifier": true,
}

// bodyNodeTypes are the AST node types that contain the "body" of a block
// (the part we want to collapse).
var bodyNodeTypes = map[string]bool{
	"block": true, "body": true, "block_statement": true,
	"statement_block": true, "compound_statement": true,
	// C/C++ struct/enum bodies
	"field_declaration_list": true, "enumerator_list": true,
}

// OutlineFile produces a depth-limited view of a source file.
// depth=1: just signatures. depth=2: bodies with blocks collapsed. depth=3+: more expansion.
func OutlineFile(path string, depth int) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return OutlineFileFromSource(path, src, depth)
}

// OutlineFileFromSource is like OutlineFile but takes pre-loaded source bytes,
// avoiding redundant file reads.
func OutlineFileFromSource(path string, src []byte, depth int) (string, error) {
	lang := GetLangConfig(path)
	if lang == nil {
		return "", fmt.Errorf("unsupported language for %s", path)
	}

	var result string
	cachedParseWith(lang, src, func(root *tree_sitter.Node) {
		if depth == 1 && (lang.LangID == "c" || lang.LangID == "cpp") {
			result = collapseCSigView(root, src)
		} else {
			result = collapseNode(root, src, depth, 0)
		}
	})
	return result, nil
}

// OutlineSymbol produces a depth-limited view of a specific symbol's source.
// depth=1: signature only. depth=2: body with blocks collapsed. etc.
func OutlineSymbol(path string, sym SymbolInfo, depth int) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return OutlineSymbolFromSource(path, sym, src, depth)
}

// OutlineSymbolFromSource is like OutlineSymbol but takes pre-loaded source bytes,
// avoiding redundant file reads.
func OutlineSymbolFromSource(path string, sym SymbolInfo, src []byte, depth int) (string, error) {
	lang := GetLangConfig(path)
	if lang == nil {
		return "", fmt.Errorf("unsupported language for %s", path)
	}

	if depth <= 1 {
		return ExtractSignatureFromSource(sym, src), nil
	}

	var result string
	cachedParseWith(lang, src, func(root *tree_sitter.Node) {
		symNode := findNodeAt(root, uint(sym.StartByte), uint(sym.EndByte))
		if symNode == nil {
			result = string(src[sym.StartByte:sym.EndByte])
		} else {
			result = collapseNode(symNode, src, depth, 0)
		}
	})
	return result, nil
}

// findNodeAt finds the most specific node spanning exactly [start, end).
func findNodeAt(node *tree_sitter.Node, startByte, endByte uint) *tree_sitter.Node {
	if node.StartByte() == startByte && node.EndByte() == endByte {
		return node
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil && child.StartByte() <= startByte && child.EndByte() >= endByte {
			result := findNodeAt(child, startByte, endByte)
			if result != nil {
				return result
			}
		}
	}
	// If no exact match, return this node if it spans the range
	if node.StartByte() <= startByte && node.EndByte() >= endByte {
		return node
	}
	return nil
}

// collapseNode renders a node's source with blocks collapsed beyond the depth limit.
// blockDepth tracks how many block-level nestings we've entered.
func collapseNode(node *tree_sitter.Node, src []byte, maxDepth int, blockDepth int) string {
	kind := node.Kind()

	// If this is a block node and we're at the depth limit, collapse it
	if blockNodeTypes[kind] && blockDepth >= maxDepth-1 {
		return collapseBlockToHeader(node, src)
	}

	// If this node has no children, return its source text
	if node.ChildCount() == 0 {
		return string(src[node.StartByte():node.EndByte()])
	}

	// Recurse into children, incrementing blockDepth for block nodes
	nextBlockDepth := blockDepth
	if blockNodeTypes[kind] {
		nextBlockDepth++
	}

	// Build output by combining children's text with gaps (whitespace/punctuation between them)
	var result strings.Builder
	lastEnd := node.StartByte()

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child == nil {
			continue
		}

		// Preserve text between children (whitespace, operators, etc.)
		if child.StartByte() > lastEnd {
			result.Write(src[lastEnd:child.StartByte()])
		}

		result.WriteString(collapseNode(child, src, maxDepth, nextBlockDepth))
		lastEnd = child.EndByte()
	}

	// Preserve trailing text after last child
	if lastEnd < node.EndByte() {
		result.Write(src[lastEnd:node.EndByte()])
	}

	return result.String()
}

// collapseBlockToHeader returns a block statement's header (condition) + "..."
// e.g., "if x > 0:\n    ..." or "for _, item := range items { ... }"
func collapseBlockToHeader(node *tree_sitter.Node, src []byte) string {
	kind := node.Kind()

	// Find the body/block child — that's what we collapse
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child == nil {
			continue
		}
		childKind := child.Kind()

		// Is this a body/block node we should collapse?
		if bodyNodeTypes[childKind] || childKind == "block" || childKind == "body" ||
			childKind == "consequence" || childKind == "alternative" {

			// Everything before the body is the header
			header := strings.TrimRight(string(src[node.StartByte():child.StartByte()]), " \t\n")

			// Detect indentation of the body content
			indent := detectBlockIndent(child, src)

			// Produce header + collapsed body
			if isBraceLanguageBlock(childKind, kind) {
				return header + " { ... }"
			}
			// Python/Ruby style: newline + indented ...
			return header + "\n" + indent + "..."
		}
	}

	// No clear body child found — show first line + ...
	full := string(src[node.StartByte():node.EndByte()])
	firstNL := strings.Index(full, "\n")
	if firstNL >= 0 {
		header := full[:firstNL]
		indent := detectIndentOfNextLine(full[firstNL+1:])
		return header + "\n" + indent + "..."
	}
	return full
}

// skipInCSig lists node types to exclude from C/C++ --sig output.
// These are preprocessor noise that clutters the API surface view.
var skipInCSig = map[string]bool{
	"#ifndef": true, "#ifdef": true, "#endif": true, "#define": true,
	"preproc_include": true, "preproc_def": true, "preproc_call": true,
	"preproc_function_def": true,
}

// collapseCSigView produces a clean API surface for C/C++ headers.
// It unwraps include-guard #ifdef/#ifndef wrappers and strips #include
// directives, showing only type definitions, declarations, and section comments.
func collapseCSigView(node *tree_sitter.Node, src []byte) string {
	var parts []string
	collectCSigParts(node, src, &parts)
	return strings.Join(parts, "\n")
}

func collectCSigParts(node *tree_sitter.Node, src []byte, parts *[]string) {
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child == nil {
			continue
		}
		kind := child.Kind()

		// Skip preprocessor noise
		if skipInCSig[kind] || kind == "identifier" {
			continue
		}

		// Unwrap include-guard wrappers — recurse into their children
		if kind == "preproc_ifdef" || kind == "preproc_ifndef" {
			collectCSigParts(child, src, parts)
			continue
		}

		if kind == "comment" {
			text := string(src[child.StartByte():child.EndByte()])
			// Skip file-level doc comments (multi-line, before any declaration)
			if len(*parts) == 0 && strings.Contains(text, "\n") {
				continue
			}
			// Skip include-guard close comments (e.g., /* HEADER_H */)
			trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text, "*/"), "/*"))
			if strings.HasSuffix(trimmed, "_H") || strings.HasSuffix(trimmed, "_H_") ||
				strings.HasSuffix(trimmed, "_HPP") || strings.HasSuffix(trimmed, "_HPP_") {
				continue
			}
		}

		// Keep declarations, type_definitions, and section comments — collapse bodies
		*parts = append(*parts, collapseNode(child, src, 1, 0))
	}
}

func isBraceLanguageBlock(childKind, parentKind string) bool {
	return childKind == "statement_block" || childKind == "block_statement" ||
		childKind == "compound_statement" ||
		// C/C++ struct/enum bodies
		childKind == "field_declaration_list" || childKind == "enumerator_list" ||
		// Go blocks
		(childKind == "block" && (parentKind == "if_statement" || parentKind == "for_statement" ||
			parentKind == "expression_switch_statement" || parentKind == "type_switch_statement" ||
			parentKind == "select_statement"))
}

func detectBlockIndent(node *tree_sitter.Node, src []byte) string {
	// Look at the first line inside the block to detect indentation
	start := node.StartByte()
	end := node.EndByte()
	if start >= end {
		return "    "
	}
	body := string(src[start:end])
	lines := strings.Split(body, "\n")
	for _, line := range lines[1:] { // skip first line (might be '{')
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && trimmed != "}" && trimmed != "end" {
			ws := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			return ws
		}
	}
	return "    "
}

func detectIndentOfNextLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		}
	}
	return "    "
}
