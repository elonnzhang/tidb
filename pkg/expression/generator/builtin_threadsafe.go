// Copyright 2024 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path"
	"sort"
	"strings"
)

var (
	specialSafeFuncs = map[string]struct{}{
		"builtinInIntSig":          {},
		"builtinInStringSig":       {},
		"builtinInRealSig":         {},
		"builtinInDecimalSig":      {},
		"builtinInTimeSig":         {},
		"builtinInDurationSig":     {},
		"builtinRealIsTrueSig":     {},
		"builtinDecimalIsTrueSig":  {},
		"builtinIntIsTrueSig":      {},
		"builtinRealIsFalseSig":    {},
		"builtinDecimalIsFalseSig": {},
		"builtinIntIsFalseSig":     {},
		// NOTE: please make sure there are test cases for all functions here.
	}
)

func collectThreadSafeBuiltinFuncs(file string) (safeFuncNames, unsafeFuncNames []string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		panic(err)
	}

	allFuncNames := make([]string, 0, 32)
	ast.Inspect(f, func(n ast.Node) bool {
		x, ok := n.(*ast.TypeSpec) // get all type definitions
		if !ok {
			return true
		}
		typeName := x.Name.Name
		if !strings.HasPrefix(typeName, "builtin") ||
			!strings.HasSuffix(typeName, "Sig") {
			return true // the type name should be "builtin*Sig"
		}
		if x.Type == nil {
			return true
		}
		structType, ok := x.Type.(*ast.StructType)
		if !ok { // the type must be a structure
			return true
		}
		allFuncNames = append(allFuncNames, typeName)
		if _, ok := specialSafeFuncs[typeName]; ok {
			safeFuncNames = append(safeFuncNames, typeName)
			return true
		}
		if len(structType.Fields.List) != 1 { // this structure only has 1 field
			return true
		}
		// this builtinXSig has only 1 field and this field is `baseBuiltinFunc` or `baseBuiltinCastFunc`.
		if ident, ok := structType.Fields.List[0].Type.(*ast.Ident); ok &&
			(ident.Name == "baseBuiltinFunc" || ident.Name == "baseBuiltinCastFunc") {
			safeFuncNames = append(safeFuncNames, typeName)
		}
		return true
	})

	safeFuncMap := make(map[string]struct{}, len(safeFuncNames))
	for _, name := range safeFuncNames {
		safeFuncMap[name] = struct{}{}
	}
	for _, fName := range allFuncNames {
		if _, ok := safeFuncMap[fName]; !ok {
			unsafeFuncNames = append(unsafeFuncNames, fName)
		}
	}

	return safeFuncNames, unsafeFuncNames
}

func genBuiltinThreadSafeCode(exprCodeDir string) (safe, unsafe []byte) {
	entries, err := os.ReadDir(exprCodeDir)
	if err != nil {
		panic(err)
	}
	files := make([]string, 0, 16)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), "builtin_") &&
			strings.HasSuffix(entry.Name(), ".go") &&
			!strings.Contains(entry.Name(), "_test") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	safeFuncs := make([]string, 0, 32)
	unsafeFuncs := make([]string, 0, 32)
	for _, file := range files {
		safeNames, unsafeNames := collectThreadSafeBuiltinFuncs(path.Join(exprCodeDir, file))
		safeFuncs = append(safeFuncs, safeNames...)
		unsafeFuncs = append(unsafeFuncs, unsafeNames...)
	}
	sort.Strings(safeFuncs)

	formattedSafe, err := generateCode(safeFuncs, safeHeader, safeFuncTemp)
	if err != nil {
		panic(err)
	}

	formattedUnsafe, err := generateCode(unsafeFuncs, unsafeHeader, unsafeFuncTemp)
	if err != nil {
		panic(err)
	}

	return formattedSafe, formattedUnsafe
}

func generateCode(funcNames []string, header, template string) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(header)
	for _, funcName := range funcNames {
		buffer.WriteString(fmt.Sprintf(template, funcName))
	}
	return format.Source(buffer.Bytes())
}

func main() {
	safeCode, unsafeCode := genBuiltinThreadSafeCode(".")
	if err := os.WriteFile("./builtin_threadsafe_generated.go", safeCode, 0644); err != nil {
		log.Fatalln("failed to write builtin_threadsafe_generated.go", err)
	}
	if err := os.WriteFile("./builtin_threadunsafe_generated.go", unsafeCode, 0644); err != nil {
		log.Fatalln("failed to write builtin_threadunsafe_generated.go", err)
	}
}

const (
	safeFuncTemp = `// SafeToShareAcrossSession implements BuiltinFunc.SafeToShareAcrossSession.
func (s *%s) SafeToShareAcrossSession() bool {
	return safeToShareAcrossSession(&s.safeToShareAcrossSessionFlag, s.args)
}
`
	unsafeFuncTemp = `// SafeToShareAcrossSession implements BuiltinFunc.SafeToShareAcrossSession.
func (s *%s) SafeToShareAcrossSession() bool {
	return false
}
`
	safeHeader = `// Copyright 2024 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by go generate in expression/generator; DO NOT EDIT.

package expression

import "sync/atomic"

func safeToShareAcrossSession(flag *uint32, args []Expression) bool {
	flagV := atomic.LoadUint32(flag)
	if flagV != 0 {
		return flagV == 1
	}

	allArgsSafe := true
	for _, arg := range args {
		if !arg.SafeToShareAcrossSession() {
			allArgsSafe = false
			break
		}
	}
	if allArgsSafe {
		atomic.StoreUint32(flag, 1)
	} else {
		atomic.StoreUint32(flag, 2)
	}
	return allArgsSafe
}

`

	unsafeHeader = `// Copyright 2024 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by go generate in expression/generator; DO NOT EDIT.

package expression

`
)
