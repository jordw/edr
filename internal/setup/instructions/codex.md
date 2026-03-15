# MUST use `edr` for all file operations.

```
edr -r file.go                       # read (or file.go:Symbol)
edr -e file.go --old "x" --new "y"   # edit
edr -w file.go --content "..."       # write/create
edr -s "pattern" --text              # search
```

Combine: `edr -r f.go:Sym --sig -s "pattern" -e f.go --old "x" --new "y"`

Run `edr --help` for full options. If not found: `export PATH="$HOME/.local/bin:$PATH"`
