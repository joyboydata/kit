package agui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// ApplyJSONPatch applies RFC 6902 operations atomically: any failed operation
// rolls the whole patch back. Unknown ops are skipped (reported by index);
// malformed patches (not an array, missing op/path) are fatal.
func ApplyJSONPatch(state any, patch any) (next any, skipped []int, err error) {
	ops, err := normalizePatch(patch)
	if err != nil {
		return state, nil, err
	}
	working := cloneAny(state)
	for i, op := range ops {
		name, _ := op["op"].(string)
		path, hasPath := op["path"].(string)
		switch name {
		case "add", "remove", "replace", "move", "copy", "test":
		case "":
			return state, skipped, fmt.Errorf("agui: patch op %d requires op", i)
		default:
			skipped = append(skipped, i) // unrecognised union member: strip
			continue
		}
		if !hasPath {
			return state, skipped, fmt.Errorf("agui: patch op %d requires path", i)
		}
		if err := applyOp(&working, name, path, op); err != nil {
			return state, skipped, fmt.Errorf("agui: patch op %d: %w", i, err)
		}
	}
	return working, skipped, nil
}

// normalizePatch accepts the decoded ([]any) and typed ([]PatchOp,
// []map[string]any) spellings of a patch array.
func normalizePatch(patch any) ([]map[string]any, error) {
	switch p := patch.(type) {
	case []any:
		ops := make([]map[string]any, 0, len(p))
		for _, raw := range p {
			op, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("agui: patch must be an array of objects")
			}
			ops = append(ops, op)
		}
		return ops, nil
	case []map[string]any:
		return p, nil
	case []PatchOp:
		ops := make([]map[string]any, 0, len(p))
		for _, o := range p {
			m := map[string]any{"op": o.Op, "path": o.Path}
			if o.Value != nil {
				m["value"] = o.Value
			}
			if o.From != "" {
				m["from"] = o.From
			}
			ops = append(ops, m)
		}
		return ops, nil
	default:
		return nil, fmt.Errorf("agui: patch must be an array of objects")
	}
}

func applyOp(doc *any, name, path string, op map[string]any) error {
	parts, err := ptrParts(path)
	if err != nil {
		return err
	}
	switch name {
	case "add":
		v, ok := op["value"]
		if !ok {
			return fmt.Errorf("add requires value")
		}
		nv, err := descend(*doc, parts, func(parent any, last string) (any, error) {
			return leafAdd(parent, last, cloneAny(v))
		})
		if err != nil {
			return err
		}
		*doc = nv
		return nil
	case "replace":
		v, ok := op["value"]
		if !ok {
			return fmt.Errorf("replace requires value")
		}
		nv, err := descend(*doc, parts, func(parent any, last string) (any, error) {
			return leafReplace(parent, last, cloneAny(v))
		})
		if err != nil {
			return err
		}
		*doc = nv
		return nil
	case "remove":
		if len(parts) == 0 {
			return fmt.Errorf("cannot remove whole document")
		}
		nv, err := descend(*doc, parts, leafRemove)
		if err != nil {
			return err
		}
		*doc = nv
		return nil
	case "move":
		from, ok := op["from"].(string)
		if !ok {
			return fmt.Errorf("move requires from")
		}
		v, err := patchGet(*doc, from)
		if err != nil {
			return err
		}
		if err := applyOp(doc, "remove", from, op); err != nil {
			return err
		}
		return applyOp(doc, "add", path, map[string]any{"value": v})
	case "copy":
		from, ok := op["from"].(string)
		if !ok {
			return fmt.Errorf("copy requires from")
		}
		v, err := patchGet(*doc, from)
		if err != nil {
			return err
		}
		return applyOp(doc, "add", path, map[string]any{"value": cloneAny(v)})
	case "test":
		v, ok := op["value"]
		if !ok {
			return fmt.Errorf("test requires value")
		}
		got, err := patchGet(*doc, path)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, v) {
			return fmt.Errorf("test failed at %q", path)
		}
		return nil
	}
	return fmt.Errorf("unknown op %q", name)
}

// ptrParts splits an RFC 6901 pointer into reference tokens.
func ptrParts(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("bad pointer %q", path)
	}
	raw := strings.Split(path[1:], "/")
	for i, tok := range raw {
		raw[i] = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
	}
	return raw, nil
}

// descend walks to the target's parent, applies leaf, and rebuilds containers
// along the path (slices change identity on insert/delete).
func descend(cur any, parts []string, leaf func(parent any, last string) (any, error)) (any, error) {
	if len(parts) == 0 {
		return leaf(nil, "") // whole-document target
	}
	if len(parts) == 1 {
		return leaf(cur, parts[0])
	}
	switch n := cur.(type) {
	case map[string]any:
		child, ok := n[parts[0]]
		if !ok {
			return nil, fmt.Errorf("no key %q", parts[0])
		}
		nv, err := descend(child, parts[1:], leaf)
		if err != nil {
			return nil, err
		}
		n[parts[0]] = nv
		return n, nil
	case []any:
		i, err := strconv.Atoi(parts[0])
		if err != nil || i < 0 || i >= len(n) {
			return nil, fmt.Errorf("no index %q", parts[0])
		}
		nv, err := descend(n[i], parts[1:], leaf)
		if err != nil {
			return nil, err
		}
		n[i] = nv
		return n, nil
	default:
		return nil, fmt.Errorf("cannot traverse %q", parts[0])
	}
}

func leafAdd(parent any, last string, v any) (any, error) {
	if parent == nil {
		return v, nil // whole-document add replaces
	}
	switch n := parent.(type) {
	case map[string]any:
		n[last] = v
		return n, nil
	case []any:
		if last == "-" {
			return append(n, v), nil
		}
		i, err := strconv.Atoi(last)
		if err != nil || i < 0 || i > len(n) {
			return nil, fmt.Errorf("no index %q", last)
		}
		out := make([]any, 0, len(n)+1)
		out = append(out, n[:i]...)
		out = append(out, v)
		out = append(out, n[i:]...)
		return out, nil
	}
	return nil, fmt.Errorf("cannot add at %q", last)
}

func leafReplace(parent any, last string, v any) (any, error) {
	if parent == nil {
		return v, nil
	}
	switch n := parent.(type) {
	case map[string]any:
		if _, ok := n[last]; !ok {
			return nil, fmt.Errorf("no key %q", last)
		}
		n[last] = v
		return n, nil
	case []any:
		i, err := strconv.Atoi(last)
		if err != nil || i < 0 || i >= len(n) {
			return nil, fmt.Errorf("no index %q", last)
		}
		n[i] = v
		return n, nil
	}
	return nil, fmt.Errorf("cannot replace at %q", last)
}

func leafRemove(parent any, last string) (any, error) {
	switch n := parent.(type) {
	case map[string]any:
		if _, ok := n[last]; !ok {
			return nil, fmt.Errorf("no key %q", last)
		}
		delete(n, last)
		return n, nil
	case []any:
		i, err := strconv.Atoi(last)
		if err != nil || i < 0 || i >= len(n) {
			return nil, fmt.Errorf("no index %q", last)
		}
		return append(n[:i], n[i+1:]...), nil
	}
	return nil, fmt.Errorf("cannot remove at %q", last)
}

func patchGet(doc any, path string) (any, error) {
	parts, err := ptrParts(path)
	if err != nil {
		return nil, err
	}
	cur := doc
	for _, tok := range parts {
		switch n := cur.(type) {
		case map[string]any:
			v, ok := n[tok]
			if !ok {
				return nil, fmt.Errorf("no key %q", tok)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(n) {
				return nil, fmt.Errorf("no index %q", tok)
			}
			cur = n[i]
		default:
			return nil, fmt.Errorf("cannot traverse %q", tok)
		}
	}
	return cur, nil
}

// cloneAny deep-copies JSON-shaped values, preserving Go scalar types.
func cloneAny(v any) any {
	switch n := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for k, e := range n {
			out[k] = cloneAny(e)
		}
		return out
	case []any:
		out := make([]any, len(n))
		for i, e := range n {
			out[i] = cloneAny(e)
		}
		return out
	default:
		return v
	}
}

// asObject normalizes one JSON object value (activity content); structs pass
// through a round-trip so callers are not forced onto map[string]any.
func asObject(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}
