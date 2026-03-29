package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegexGo_Basic(t *testing.T) {
	src := `package main

import "fmt"

type Server struct {
	addr string
	port int
}

func NewServer(addr string) *Server {
	return &Server{addr: addr}
}

func (srv *Server) Start() error {
	fmt.Println("starting", srv.addr)
	return nil
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

type Handler interface {
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

var defaultTimeout = 30

func helper() string {
	return "hi"
}
`
	syms := RegexParse("main.go", []byte(src))

	expected := []struct {
		name  string
		typ   string
		start uint32
	}{
		{"Server", "struct", 5},
		{"NewServer", "function", 10},
		{"Start", "method", 14},
		{"handleRequest", "method", 19},
		{"Handler", "interface", 23},
		{"defaultTimeout", "variable", 27},
		{"helper", "function", 29},
	}

	if len(syms) != len(expected) {
		for _, s := range syms {
			t.Logf("  got: %s %s @ %d-%d", s.Type, s.Name, s.StartLine, s.EndLine)
		}
		t.Fatalf("expected %d symbols, got %d", len(expected), len(syms))
	}

	for i, e := range expected {
		s := syms[i]
		if s.Name != e.name || s.Type != e.typ || s.StartLine != e.start {
			t.Errorf("symbol %d: expected %s %s @%d, got %s %s @%d",
				i, e.typ, e.name, e.start, s.Type, s.Name, s.StartLine)
		}
	}

	// Multi-char receiver names should work
	if syms[2].Name != "Start" {
		t.Errorf("expected Start method, got %s", syms[2].Name)
	}
	// Server struct ends at line 8
	if syms[0].EndLine != 8 {
		t.Errorf("Server struct should end at 8, got %d", syms[0].EndLine)
	}
}

func TestRegexGo_MultilineSig(t *testing.T) {
	src := `package main

func LongFunc(
	arg1 string,
	arg2 int,
	arg3 bool,
) error {
	return nil
}
`
	syms := RegexParse("main.go", []byte(src))
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	if syms[0].EndLine != 9 {
		t.Errorf("expected end at 9, got %d", syms[0].EndLine)
	}
}

func TestRegexPython_Basic(t *testing.T) {
	src := `class UserService:
    def __init__(self, db):
        self.db = db

    def get_user(self, user_id: int) -> dict:
        """Fetch a user by ID."""
        return self.db.query(user_id)

    def _validate(self, data):
        if not data:
            raise ValueError("empty")

def standalone_func(x, y):
    return x + y
`
	syms := RegexParse("service.py", []byte(src))

	if len(syms) < 4 {
		for _, s := range syms {
			t.Logf("  got: %s %s @ %d-%d parent=%d", s.Type, s.Name, s.StartLine, s.EndLine, s.ParentIndex)
		}
		t.Fatalf("expected at least 4 symbols, got %d", len(syms))
	}

	if syms[0].Name != "UserService" || syms[0].Type != "class" {
		t.Errorf("expected class UserService, got %s %s", syms[0].Type, syms[0].Name)
	}

	// Methods should have parent index pointing to the class
	for _, s := range syms[1:] {
		if s.Type == "method" && s.ParentIndex != 0 {
			t.Errorf("%s should have parent index 0 (UserService), got %d", s.Name, s.ParentIndex)
		}
	}

	// standalone_func should have no parent
	last := syms[len(syms)-1]
	if last.Name != "standalone_func" || last.ParentIndex != -1 {
		t.Errorf("standalone_func should have no parent, got parentIdx=%d", last.ParentIndex)
	}
}

func TestRegexTypeScript_Basic(t *testing.T) {
	src := `interface User {
  id: number;
  name: string;
}

export class UserStore {
  private cache = new Map<number, User>();

  async getUser(id: number): Promise<User> {
    if (this.cache.has(id)) return this.cache.get(id)!;
    const user = await fetch('/api').then(r => r.json());
    this.cache.set(id, user);
    return user;
  }

  invalidate(id: number): void {
    this.cache.delete(id);
  }
}

export function processUsers(store: UserStore, ids: number[]) {
  return Promise.all(ids.map(id => store.getUser(id)));
}

type UserId = number;
`
	syms := RegexParse("store.ts", []byte(src))

	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}

	for _, want := range []string{"User", "UserStore", "processUsers", "UserId"} {
		if !names[want] {
			t.Errorf("missing symbol: %s", want)
		}
	}

	// Should NOT have "if" as a method
	if names["if"] {
		t.Error("should not match 'if' keyword as method")
	}
}

func TestRegexRust_WhereClause(t *testing.T) {
	src := `pub fn process<T, S>(
    input: T,
    sink: S,
) -> Result<(), S::Error>
where
    T: AsRef<[u8]>,
    S: Handler,
{
    sink.handle(input.as_ref())?;
    Ok(())
}
`
	syms := RegexParse("lib.rs", []byte(src))
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	if syms[0].Name != "process" {
		t.Errorf("expected process, got %s", syms[0].Name)
	}
	if syms[0].EndLine != 11 {
		t.Errorf("expected end at 11 (where clause), got %d", syms[0].EndLine)
	}
}

func TestRegexByteOffsets(t *testing.T) {
	src := "line1\nline2\nfunc foo() {\n}\n"
	syms := RegexParse("test.go", []byte(src))
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	// "line1\nline2\n" = 12 bytes, so func starts at byte 12
	if syms[0].StartByte != 12 {
		t.Errorf("expected StartByte=12, got %d", syms[0].StartByte)
	}
	// "func foo() {\n}" = 15 bytes, end at byte 26 (12+15-1)
	if syms[0].EndByte != 26 {
		t.Errorf("expected EndByte=26, got %d", syms[0].EndByte)
	}
}

func TestRegexUnsupportedExtension(t *testing.T) {
	if RegexSupported("README.md") {
		t.Error("markdown should not be supported")
	}
	if RegexSupported("script.sh") {
		t.Error("shell should not be supported (no patterns yet)")
	}
	syms := RegexParse("README.md", []byte("# Hello"))
	if syms != nil {
		t.Error("should return nil for unsupported files")
	}
}

// TestRegexGo_RepoAccuracy compares regex extraction against the edr codebase itself.
func TestRegexGo_RepoAccuracy(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Skip("cannot get working dir")
	}
	// Walk up to repo root
	for !fileExists(filepath.Join(root, "go.mod")) {
		parent := filepath.Dir(root)
		if parent == root {
			t.Skip("not in edr repo")
		}
		root = parent
	}

	var totalSyms int
	var brokenEnd int
	var files int

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.Contains(path, "internal/grammars") || strings.Contains(path, "testdata") {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".go") || info.IsDir() {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		syms := RegexParse(path, src)
		files++
		for _, s := range syms {
			totalSyms++
			if s.EndLine < s.StartLine {
				brokenEnd++
				t.Errorf("broken end line: %s:%s @ %d-%d", path, s.Name, s.StartLine, s.EndLine)
			}
			// Verify byte offsets are within file bounds
			if int(s.EndByte) > len(src) {
				t.Errorf("EndByte out of bounds: %s:%s EndByte=%d fileLen=%d", path, s.Name, s.EndByte, len(src))
			}
		}
		return nil
	})

	t.Logf("Parsed %d Go files, found %d symbols, %d broken end lines", files, totalSyms, brokenEnd)

	if files < 50 {
		t.Errorf("expected to parse at least 50 Go files, got %d", files)
	}
	if totalSyms < 1000 {
		t.Errorf("expected at least 1000 symbols, got %d", totalSyms)
	}
}


func TestRegexCSharp_Basic(t *testing.T) {
	src := `using System;

namespace MyApp.Services
{
    public sealed class UserService : IUserService
    {
        private readonly IDatabase _db;

        public UserService(IDatabase db)
        {
            _db = db;
        }

        public async Task<User> GetUserAsync(int id)
        {
            return await _db.FindAsync(id);
        }

        public static UserService Create() => new UserService(new Database());
    }

    public interface IUserService
    {
        Task<User> GetUserAsync(int id);
    }

    public record UserDto(string Name, int Age);
}
`
	syms := RegexParse("Service.cs", []byte(src))
	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Type
	}
	for _, want := range []string{"MyApp.Services", "UserService", "IUserService", "UserDto", "GetUserAsync"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing: %s (got %v)", want, names)
		}
	}
	// Constructor
	found := false
	for _, s := range syms {
		if s.Name == "UserService" && s.Type == "method" {
			found = true
		}
	}
	if !found {
		t.Error("missing constructor UserService(...)")
	}
}

func TestRegexKotlin_Basic(t *testing.T) {
	src := `package com.example

data class User(val name: String, val age: Int)

sealed class Result<out T> {
    data class Success<T>(val data: T) : Result<T>()
    data class Error(val message: String) : Result<Nothing>()
}

interface Repository {
    suspend fun findById(id: Int): User?
}

class UserRepository(private val db: Database) : Repository {
    override suspend fun findById(id: Int): User? {
        return db.query(id)
    }

    fun findAll(): List<User> {
        return db.queryAll()
    }
}
`
	syms := RegexParse("User.kt", []byte(src))
	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Type
	}
	for _, want := range []string{"User", "Result", "Repository", "UserRepository", "findById", "findAll"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing: %s (got %v)", want, names)
		}
	}
}

func TestRegexSwift_Basic(t *testing.T) {
	src := `import Foundation

protocol Fetchable {
    func fetch(id: Int) async throws -> Data
}

public final class APIClient: Fetchable {
    private let session: URLSession

    public init(session: URLSession = .shared) {
        self.session = session
    }

    public func fetch(id: Int) async throws -> Data {
        let url = URL(string: "https://api.example.com/\(id)")!
        let (data, _) = try await session.data(from: url)
        return data
    }

    static func create() -> APIClient {
        return APIClient()
    }
}

struct Config {
    let timeout: TimeInterval
    let retries: Int
}

enum Status {
    case active
    case inactive
    case error(String)
}

extension APIClient {
    func healthCheck() async -> Bool {
        return true
    }
}
`
	syms := RegexParse("API.swift", []byte(src))
	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Type
	}
	for _, want := range []string{"Fetchable", "APIClient", "Config", "Status", "init", "fetch", "create", "healthCheck"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing: %s (got %v)", want, names)
		}
	}
}

func TestRegexPHP_Basic(t *testing.T) {
	src := `<?php

namespace App\Services;

abstract class BaseService
{
    abstract protected function validate($data): bool;
}

interface UserServiceInterface
{
    public function getUser(int $id): ?User;
}

final class UserService extends BaseService implements UserServiceInterface
{
    private $db;

    public function __construct(Database $db)
    {
        $this->db = $db;
    }

    public function getUser(int $id): ?User
    {
        return $this->db->find($id);
    }

    protected function validate($data): bool
    {
        return !empty($data);
    }

    public static function create(): self
    {
        return new self(new Database());
    }
}

function helper(): string
{
    return "hello";
}
`
	syms := RegexParse("UserService.php", []byte(src))
	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Type
	}
	for _, want := range []string{"BaseService", "UserServiceInterface", "UserService", "__construct", "getUser", "validate", "create", "helper"} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing: %s (got %v)", want, names)
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRegexGo_LongMultilineSig(t *testing.T) {
	// Signatures that push { beyond 20 lines (the old hard cap).
	src := `package main

func VeryLong(
	arg1 string,
	arg2 string,
	arg3 string,
	arg4 string,
	arg5 string,
	arg6 string,
	arg7 string,
	arg8 string,
	arg9 string,
	arg10 string,
	arg11 string,
	arg12 string,
	arg13 string,
	arg14 string,
	arg15 string,
	arg16 string,
	arg17 string,
	arg18 string,
	arg19 string,
	arg20 string,
) error {
	return nil
}
`
	syms := RegexParse("main.go", []byte(src))
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	// { is on line 24, body ends at 26. Old 20-line cap would miss it.
	if syms[0].EndLine != 26 {
		t.Errorf("expected end at 26, got %d (long signature truncated?)", syms[0].EndLine)
	}
}

func TestRegexRust_LongWhereClause(t *testing.T) {
	src := `pub fn complex<A, B, C, D, E, F, G, H>(
    a: A,
    b: B,
    c: C,
    d: D,
    e: E,
    f: F,
    g: G,
    h: H,
) -> Result<(), Error>
where
    A: Clone + Send + Sync,
    B: Clone + Send + Sync,
    C: Clone + Send + Sync,
    D: Clone + Send + Sync,
    E: Clone + Send + Sync,
    F: Clone + Send + Sync,
    G: Clone + Send + Sync,
    H: Clone + Send + Sync,
{
    Ok(())
}
`
	syms := RegexParse("lib.rs", []byte(src))
	if len(syms) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	if syms[0].EndLine != 22 {
		t.Errorf("expected end at 22, got %d (where clause too long?)", syms[0].EndLine)
	}
}

func TestRegexTS_RegexLiteral(t *testing.T) {
	// Regex literals containing braces should not corrupt brace depth.
	src := `export function sanitize(input: string): string {
  const cleaned = input.replace(/[{}]/g, '');
  const pattern = /foo{1,3}/;
  if (pattern.test(cleaned)) {
    return cleaned;
  }
  return input;
}
`
	syms := RegexParse("util.ts", []byte(src))
	if len(syms) != 1 {
		for _, s := range syms {
			t.Logf("  %s %s @ %d-%d", s.Type, s.Name, s.StartLine, s.EndLine)
		}
		t.Fatalf("expected 1 symbol, got %d", len(syms))
	}
	if syms[0].EndLine != 8 {
		t.Errorf("expected end at 8, got %d (regex literal corrupted brace count?)", syms[0].EndLine)
	}
}

func TestRegexJava_Constructor(t *testing.T) {
	src := `public class UserService {
    private final Database db;

    public UserService(Database db) {
        this.db = db;
    }

    public User getUser(int id) {
        return db.find(id);
    }
}
`
	syms := RegexParse("UserService.java", []byte(src))

	names := map[string]string{}
	for _, s := range syms {
		names[s.Name] = s.Type
	}

	if _, ok := names["UserService"]; !ok {
		t.Error("missing class UserService")
	}
	// The constructor should be found as a method named UserService
	constructorFound := false
	for _, s := range syms {
		if s.Name == "UserService" && s.Type == "method" {
			constructorFound = true
			break
		}
	}
	if !constructorFound {
		for _, s := range syms {
			t.Logf("  %s %s @ %d-%d", s.Type, s.Name, s.StartLine, s.EndLine)
		}
		t.Error("missing constructor UserService(...)")
	}
	if _, ok := names["getUser"]; !ok {
		t.Error("missing method getUser")
	}
}
