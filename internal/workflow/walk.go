package workflow

import (
	"reflect"

	"github.com/dop251/goja/ast"
)

// walk visits every ast.Node reachable from root in source order. fn returns
// false to stop descending into the current node. Reflection keeps the walker
// complete across goja's node set without enumerating every struct.
func walk(root ast.Node, fn func(ast.Node) bool) {
	var visit func(v reflect.Value)
	visit = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Interface:
			if v.IsNil() {
				return
			}
			visit(v.Elem())
		case reflect.Pointer:
			if v.IsNil() {
				return
			}
			if n, ok := v.Interface().(ast.Node); ok {
				if !fn(n) {
					return
				}
			}
			visit(v.Elem())
		case reflect.Struct:
			for i := range v.NumField() {
				f := v.Field(i)
				if !f.CanInterface() {
					continue
				}
				visit(f)
			}
		case reflect.Slice:
			for i := range v.Len() {
				visit(v.Index(i))
			}
		default:
		}
	}
	visit(reflect.ValueOf(root))
}
