// Copyright 2017 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package compiler

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/syzkaller/pkg/ast"
)

type ConstInfo struct {
	Consts   []string
	Includes []string
	Incdirs  []string
	Defines  map[string]string
}

// ExtractConsts returns list of literal constants and other info required const value extraction.
func ExtractConsts(desc *ast.Description, eh0 ast.ErrorHandler) *ConstInfo {
	errors := 0
	eh := func(pos ast.Pos, msg string, args ...interface{}) {
		errors++
		msg = fmt.Sprintf(msg, args...)
		if eh0 != nil {
			eh0(pos, msg)
		} else {
			ast.LoggingHandler(pos, msg)
		}
	}
	info := &ConstInfo{
		Defines: make(map[string]string),
	}
	includeMap := make(map[string]bool)
	incdirMap := make(map[string]bool)
	constMap := make(map[string]bool)

	ast.Walk(desc, func(n1 ast.Node) {
		switch n := n1.(type) {
		case *ast.Include:
			file := n.File.Value
			if includeMap[file] {
				eh(n.Pos, "duplicate include %q", file)
			}
			includeMap[file] = true
			info.Includes = append(info.Includes, file)
		case *ast.Incdir:
			dir := n.Dir.Value
			if incdirMap[dir] {
				eh(n.Pos, "duplicate incdir %q", dir)
			}
			incdirMap[dir] = true
			info.Incdirs = append(info.Incdirs, dir)
		case *ast.Define:
			v := fmt.Sprint(n.Value.Value)
			switch {
			case n.Value.CExpr != "":
				v = n.Value.CExpr
			case n.Value.Ident != "":
				v = n.Value.Ident
			}
			name := n.Name.Name
			if info.Defines[name] != "" {
				eh(n.Pos, "duplicate define %v", name)
			}
			info.Defines[name] = v
			constMap[name] = true
		case *ast.Call:
			if !strings.HasPrefix(n.CallName, "syz_") {
				constMap["__NR_"+n.CallName] = true
			}
		case *ast.Type:
			if c := typeConstIdentifier(n); c != nil {
				constMap[c.Ident] = true
				constMap[c.Ident2] = true
			}
		case *ast.Int:
			constMap[n.Ident] = true
		}
	})

	if errors != 0 {
		return nil
	}
	info.Consts = toArray(constMap)
	return info
}

func SerializeConsts(consts map[string]uint64) []byte {
	var nv []nameValuePair
	for k, v := range consts {
		nv = append(nv, nameValuePair{k, v})
	}
	sort.Sort(nameValueArray(nv))

	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "# AUTOGENERATED FILE\n")
	for _, x := range nv {
		fmt.Fprintf(buf, "%v = %v\n", x.name, x.val)
	}
	return buf.Bytes()
}

func DeserializeConsts(data []byte, file string, eh ast.ErrorHandler) map[string]uint64 {
	consts := make(map[string]uint64)
	pos := ast.Pos{
		File: file,
		Line: 1,
	}
	ok := true
	s := bufio.NewScanner(bytes.NewReader(data))
	for ; s.Scan(); pos.Line++ {
		line := s.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq == -1 {
			eh(pos, "expect '='")
			ok = false
			continue
		}
		name := strings.TrimSpace(line[:eq])
		val, err := strconv.ParseUint(strings.TrimSpace(line[eq+1:]), 0, 64)
		if err != nil {
			eh(pos, fmt.Sprintf("failed to parse int: %v", err))
			ok = false
			continue
		}
		if _, ok := consts[name]; ok {
			eh(pos, fmt.Sprintf("duplicate const %q", name))
			ok = false
			continue
		}
		consts[name] = val
	}
	if err := s.Err(); err != nil {
		eh(pos, fmt.Sprintf("failed to parse: %v", err))
		ok = false
	}
	if !ok {
		return nil
	}
	return consts
}

func DeserializeConstsGlob(glob string, eh ast.ErrorHandler) map[string]uint64 {
	if eh == nil {
		eh = ast.LoggingHandler
	}
	files, err := filepath.Glob(glob)
	if err != nil {
		eh(ast.Pos{}, fmt.Sprintf("failed to find const files: %v", err))
		return nil
	}
	if len(files) == 0 {
		eh(ast.Pos{}, fmt.Sprintf("no const files matched by glob %q", glob))
		return nil
	}
	consts := make(map[string]uint64)
	for _, f := range files {
		data, err := ioutil.ReadFile(f)
		if err != nil {
			eh(ast.Pos{}, fmt.Sprintf("failed to read const file: %v", err))
			return nil
		}
		consts1 := DeserializeConsts(data, filepath.Base(f), eh)
		if consts1 == nil {
			consts = nil
		}
		if consts != nil {
			for n, v := range consts1 {
				if old, ok := consts[n]; ok && old != v {
					eh(ast.Pos{}, fmt.Sprintf(
						"different values for const %q: %v vs %v", n, v, old))
					return nil
				}
				consts[n] = v
			}
		}
	}
	return consts
}

type nameValuePair struct {
	name string
	val  uint64
}

type nameValueArray []nameValuePair

func (a nameValueArray) Len() int           { return len(a) }
func (a nameValueArray) Less(i, j int) bool { return a[i].name < a[j].name }
func (a nameValueArray) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }