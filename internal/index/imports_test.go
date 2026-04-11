package index

import (
	"testing"
)

func TestExtractImports_C(t *testing.T) {
	src := []byte(`
#include <linux/module.h>
#include "sched.h"
#include <asm/types.h>

void foo(void) {}
`)
	imports := ExtractImports(src, ".c")
	want := map[string]bool{
		"linux/module.h": true,
		"sched.h":        true,
		"asm/types.h":    true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_Go(t *testing.T) {
	src := []byte(`package main

import "fmt"
import foo "github.com/foo/bar"
`)
	imports := ExtractImports(src, ".go")
	want := map[string]bool{
		"fmt":                    true,
		"github.com/foo/bar":    true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_Python(t *testing.T) {
	src := []byte(`
from torch.autograd import grad
import os
from collections import Counter
`)
	imports := ExtractImports(src, ".py")
	want := map[string]bool{
		"torch.autograd": true,
		"os":             true,
		"collections":    true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_TypeScript(t *testing.T) {
	src := []byte(`
import { useState } from 'react'
import * as path from "path"
const fs = require('fs')
`)
	imports := ExtractImports(src, ".ts")
	want := map[string]bool{
		"react": true,
		"path":  true,
		"fs":    true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_Rust(t *testing.T) {
	src := []byte(`
use tokio::runtime::Runtime;
use std::collections::HashMap;
`)
	imports := ExtractImports(src, ".rs")
	want := map[string]bool{
		"tokio::runtime::Runtime":   true,
		"std::collections::HashMap": true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_Ruby(t *testing.T) {
	src := []byte(`
require 'active_record'
require "json"
`)
	imports := ExtractImports(src, ".rb")
	want := map[string]bool{
		"active_record": true,
		"json":          true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_Java(t *testing.T) {
	src := []byte(`
package org.example;

import org.springframework.context.ApplicationContext;
import java.util.List;
`)
	imports := ExtractImports(src, ".java")
	want := map[string]bool{
		"org.springframework.context.ApplicationContext": true,
		"java.util.List": true,
	}
	got := map[string]bool{}
	for _, imp := range imports {
		got[imp.Raw] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q", w)
		}
	}
}

func TestExtractImports_UnsupportedExt(t *testing.T) {
	src := []byte(`some random content`)
	imports := ExtractImports(src, ".txt")
	if len(imports) != 0 {
		t.Errorf("expected 0 imports for .txt, got %d", len(imports))
	}
}

func TestExtractImports_Dedup(t *testing.T) {
	src := []byte(`
#include "foo.h"
#include "foo.h"
`)
	imports := ExtractImports(src, ".c")
	if len(imports) != 1 {
		t.Errorf("expected 1 deduped import, got %d", len(imports))
	}
}

func TestBuildSuffixIndex(t *testing.T) {
	files := []string{
		"include/linux/sched.h",
		"kernel/sched/sched.h",
		"drivers/media/dvb/mxl5xx.c",
	}
	idx := BuildSuffixIndex(files)

	// "sched.h" should match both
	matches := idx["sched.h"]
	if len(matches) != 2 {
		t.Errorf("sched.h: expected 2 matches, got %d", len(matches))
	}

	// Full path should match exactly one
	matches = idx["include/linux/sched.h"]
	if len(matches) != 1 || matches[0] != "include/linux/sched.h" {
		t.Errorf("full path: expected exact match, got %v", matches)
	}

	// "mxl5xx.c" should match one
	matches = idx["mxl5xx.c"]
	if len(matches) != 1 {
		t.Errorf("mxl5xx.c: expected 1 match, got %d", len(matches))
	}
}

func TestResolveImport_C(t *testing.T) {
	files := []string{
		"include/linux/sched.h",
		"kernel/sched/sched.h",
		"kernel/sched/core.c",
	}
	idx := BuildSuffixIndex(files)

	// C include resolves via suffix
	matches := ResolveImport(idx, "linux/sched.h", "kernel/sched/core.c", ".c")
	if len(matches) != 1 || matches[0] != "include/linux/sched.h" {
		t.Errorf("expected include/linux/sched.h, got %v", matches)
	}

	// Short form matches multiple
	matches = ResolveImport(idx, "sched.h", "kernel/sched/core.c", ".c")
	if len(matches) != 2 {
		t.Errorf("expected 2 matches for sched.h, got %d", len(matches))
	}
}

func TestResolveImport_Python(t *testing.T) {
	files := []string{
		"torch/autograd/__init__.py",
		"torch/autograd/function.py",
	}
	idx := BuildSuffixIndex(files)

	matches := ResolveImport(idx, "torch.autograd", "", ".py")
	if len(matches) != 1 || matches[0] != "torch/autograd/__init__.py" {
		t.Errorf("expected torch/autograd/__init__.py, got %v", matches)
	}
}

func TestResolveImport_Java(t *testing.T) {
	files := []string{
		"src/main/java/org/springframework/context/ApplicationContext.java",
	}
	idx := BuildSuffixIndex(files)

	matches := ResolveImport(idx, "org.springframework.context.ApplicationContext", "", ".java")
	if len(matches) != 1 {
		t.Errorf("expected 1 match, got %d: %v", len(matches), matches)
	}
}
