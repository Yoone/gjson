// Copyright 2024 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.
//
// https://github.com/tidwall/gjson

// Package gjson provides searching for json strings.
package gjson

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/tidwall/match"
	"github.com/tidwall/pretty"
)

// Type is Result type
type Type int

const (
	// Null is a null json value
	Null Type = iota
	// False is a json false boolean
	False
	// Number is json number
	Number
	// String is a json string
	String
	// True is a json true boolean
	True
	// JSON is a raw block of JSON
	JSON
)

// String returns a string representation of the type.
func (t Type) String() string {
	switch t {
	default:
		return ""
	case Null:
		return "Null"
	case False:
		return "False"
	case Number:
		return "Number"
	case String:
		return "String"
	case True:
		return "True"
	case JSON:
		return "JSON"
	}
}

// Result represents a json value that is returned from Get().
type Result struct {
	// Type is the json type
	Type Type
	// Raw is the raw json
	Raw string
	// Str is the json string
	Str string
	// Num is the json number
	Num float64
	// Index of raw value in original json, zero means index unknown
	Index int
	// Indexes of all the elements that match on a path containing the '#'
	// query character.
	Indexes []int
}

// String returns a string representation of the value.
func (t Result) String() string {
	switch t.Type {
	default:
		return ""
	case False:
		return "false"
	case Number:
		if len(t.Raw) == 0 {
			// calculated result
			return strconv.FormatFloat(t.Num, 'f', -1, 64)
		}
		var i int
		if t.Raw[0] == '-' {
			i++
		}
		for ; i < len(t.Raw); i++ {
			if t.Raw[i] < '0' || t.Raw[i] > '9' {
				return strconv.FormatFloat(t.Num, 'f', -1, 64)
			}
		}
		return t.Raw
	case String:
		return t.Str
	case JSON:
		return t.Raw
	case True:
		return "true"
	}
}

// Bytes returns the value as a byte slice (no copy).
func (t Result) Bytes() []byte {
	switch t.Type {
	default:
		return stringBytes(t.Raw)
	case String:
		return stringBytes(t.Str)
	}
}

// Bool returns an boolean representation.
func (t Result) Bool() bool {
	switch t.Type {
	default:
		return false
	case True:
		return true
	case String:
		b, _ := strconv.ParseBool(strings.ToLower(t.Str))
		return b
	case Number:
		return t.Num != 0
	}
}

// Int returns an integer representation.
func (t Result) Int() int64 {
	switch t.Type {
	default:
		return 0
	case True:
		return 1
	case String:
		n, _ := parseInt(t.Str)
		return n
	case Number:
		// try to directly convert the float64 to int64
		i, ok := safeInt(t.Num)
		if ok {
			return i
		}
		// now try to parse the raw string
		i, ok = parseInt(t.Raw)
		if ok {
			return i
		}
		// fallback to a standard conversion
		return int64(t.Num)
	}
}

// Uint returns an unsigned integer representation.
func (t Result) Uint() uint64 {
	switch t.Type {
	default:
		return 0
	case True:
		return 1
	case String:
		n, _ := parseUint(t.Str)
		return n
	case Number:
		// try to directly convert the float64 to uint64
		i, ok := safeInt(t.Num)
		if ok && i >= 0 {
			return uint64(i)
		}
		// now try to parse the raw string
		u, ok := parseUint(t.Raw)
		if ok {
			return u
		}
		// fallback to a standard conversion
		return uint64(t.Num)
	}
}

// Float returns an float64 representation.
func (t Result) Float() float64 {
	switch t.Type {
	default:
		return 0
	case True:
		return 1
	case String:
		n, _ := strconv.ParseFloat(t.Str, 64)
		return n
	case Number:
		return t.Num
	}
}

// Time returns a time.Time representation.
func (t Result) Time() time.Time {
	res, _ := time.Parse(time.RFC3339, t.String())
	return res
}

// Array returns back an array of values.
// If the result represents a null value or is non-existent, then an empty
// array will be returned.
// If the result is not a JSON array, the return value will be an
// array containing one result.
func (t Result) Array() []Result {
	if t.Type == Null {
		return []Result{}
	}
	if !t.IsArray() {
		return []Result{t}
	}
	r := t.arrayOrMap('[', false)
	return r.a
}

// IsObject returns true if the result value is a JSON object.
func (t Result) IsObject() bool {
	return t.Type == JSON && len(t.Raw) > 0 && t.Raw[0] == '{'
}

// IsArray returns true if the result value is a JSON array.
func (t Result) IsArray() bool {
	return t.Type == JSON && len(t.Raw) > 0 && t.Raw[0] == '['
}

// IsBool returns true if the result value is a JSON boolean.
func (t Result) IsBool() bool {
	return t.Type == True || t.Type == False
}

// ForEach iterates through values.
// If the result represents a non-existent value, then no values will be
// iterated. If the result is an Object, the iterator will pass the key and
// value of each item. If the result is an Array, the iterator will only pass
// the value of each item. If the result is not a JSON array or object, the
// iterator will pass back one value equal to the result.
func (t Result) ForEach(iterator func(key, value Result) bool) {
	if !t.Exists() {
		return
	}
	if t.Type != JSON {
		iterator(Result{}, t)
		return
	}
	json := t.Raw
	var obj bool
	var i int
	var key, value Result
	for ; i < len(json); i++ {
		if json[i] == '{' {
			i++
			key.Type = String
			obj = true
			break
		} else if json[i] == '[' {
			i++
			key.Type = Number
			key.Num = -1
			break
		}
		if json[i] > ' ' {
			return
		}
	}
	var str string
	var vesc bool
	var ok bool
	var idx int
	for ; i < len(json); i++ {
		if obj {
			if json[i] != '"' {
				continue
			}
			s := i
			i, str, vesc, ok = parseString(json, i+1)
			if !ok {
				return
			}
			if vesc {
				key.Str = unescape(str[1 : len(str)-1])
			} else {
				key.Str = str[1 : len(str)-1]
			}
			key.Raw = str
			key.Index = s + t.Index
		} else {
			key.Num += 1
		}
		for ; i < len(json); i++ {
			if json[i] <= ' ' || json[i] == ',' || json[i] == ':' {
				continue
			}
			break
		}
		s := i
		i, value, ok = parseAny(json, i, true)
		if !ok {
			return
		}
		if t.Indexes != nil {
			if idx < len(t.Indexes) {
				value.Index = t.Indexes[idx]
			}
		} else {
			value.Index = s + t.Index
		}
		if !iterator(key, value) {
			return
		}
		idx++
	}
}

// Map returns back a map of values. The result should be a JSON object.
// If the result is not a JSON object, the return value will be an empty map.
func (t Result) Map() map[string]Result {
	if t.Type != JSON {
		return map[string]Result{}
	}
	r := t.arrayOrMap('{', false)
	return r.o
}

// Get searches result for the specified path.
// The result should be a JSON array or object.
func (t Result) Get(path string) Result {
	r := Get(t.Raw, path)
	if r.Indexes != nil {
		for i := 0; i < len(r.Indexes); i++ {
			r.Indexes[i] += t.Index
		}
	} else {
		r.Index += t.Index
	}
	return r
}

type arrayOrMapResult struct {
	a  []Result
	ai []interface{}
	o  map[string]Result
	oi map[string]interface{}
	vc byte
}

func (t Result) arrayOrMap(vc byte, valueize bool) (r arrayOrMapResult) {
	var json = t.Raw
	var i int
	var value Result
	var count int
	var key Result
	if vc == 0 {
		for ; i < len(json); i++ {
			if json[i] == '{' || json[i] == '[' {
				r.vc = json[i]
				i++
				break
			}
			if json[i] > ' ' {
				goto end
			}
		}
	} else {
		for ; i < len(json); i++ {
			if json[i] == vc {
				i++
				break
			}
			if json[i] > ' ' {
				goto end
			}
		}
		r.vc = vc
	}
	if r.vc == '{' {
		if valueize {
			r.oi = make(map[string]interface{})
		} else {
			r.o = make(map[string]Result)
		}
	} else {
		if valueize {
			r.ai = make([]interface{}, 0)
		} else {
			r.a = make([]Result, 0)
		}
	}
	for ; i < len(json); i++ {
		if json[i] <= ' ' {
			continue
		}
		// get next value
		if json[i] == ']' || json[i] == '}' {
			break
		}
		switch json[i] {
		default:
			if (json[i] >= '0' && json[i] <= '9') || json[i] == '-' {
				value.Type = Number
				value.Raw, value.Num = tonum(json[i:])
				value.Str = ""
			} else {
				continue
			}
		case '{', '[':
			value.Type = JSON
			value.Raw = squash(json[i:])
			value.Str, value.Num = "", 0
		case 'n':
			value.Type = Null
			value.Raw = tolit(json[i:])
			value.Str, value.Num = "", 0
		case 't':
			value.Type = True
			value.Raw = tolit(json[i:])
			value.Str, value.Num = "", 0
		case 'f':
			value.Type = False
			value.Raw = tolit(json[i:])
			value.Str, value.Num = "", 0
		case '"':
			value.Type = String
			value.Raw, value.Str = tostr(json[i:])
			value.Num = 0
		}
		value.Index = i + t.Index

		i += len(value.Raw) - 1

		if r.vc == '{' {
			if count%2 == 0 {
				key = value
			} else {
				if valueize {
					if _, ok := r.oi[key.Str]; !ok {
						r.oi[key.Str] = value.Value()
					}
				} else {
					if _, ok := r.o[key.Str]; !ok {
						r.o[key.Str] = value
					}
				}
			}
			count++
		} else {
			if valueize {
				r.ai = append(r.ai, value.Value())
			} else {
				r.a = append(r.a, value)
			}
		}
	}
end:
	if t.Indexes != nil {
		if len(t.Indexes) != len(r.a) {
			for i := 0; i < len(r.a); i++ {
				r.a[i].Index = 0
			}
		} else {
			for i := 0; i < len(r.a); i++ {
				r.a[i].Index = t.Indexes[i]
			}
		}
	}
	return
}

// Parse parses the json and returns a result.
//
// This function expects that the json is well-formed, and does not validate.
// Invalid json will not panic, but it may return back unexpected results.
// If you are consuming JSON from an unpredictable source then you may want to
// use the Valid function first.
func Parse(json string) Result {
	var value Result
	i := 0
	for ; i < len(json); i++ {
		if json[i] == '{' || json[i] == '[' {
			value.Type = JSON
			value.Raw = json[i:] // just take the entire raw
			break
		}
		if json[i] <= ' ' {
			continue
		}
		switch json[i] {
		case '+', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
			'i', 'I', 'N':
			value.Type = Number
			value.Raw, value.Num = tonum(json[i:])
		case 'n':
			if i+1 < len(json) && json[i+1] != 'u' {
				// nan
				value.Type = Number
				value.Raw, value.Num = tonum(json[i:])
			} else {
				// null
				value.Type = Null
				value.Raw = tolit(json[i:])
			}
		case 't':
			value.Type = True
			value.Raw = tolit(json[i:])
		case 'f':
			value.Type = False
			value.Raw = tolit(json[i:])
		case '"':
			value.Type = String
			value.Raw, value.Str = tostr(json[i:])
		default:
			return Result{}
		}
		break
	}
	if value.Exists() {
		value.Index = i
	}
	return value
}

// ParseBytes parses the json and returns a result.
// If working with bytes, this method is preferred over Parse(string(data))
func ParseBytes(json []byte) Result {
	return Parse(bytesString(json))
}

func squash(json string) string {
	// expects that the lead character is a '[' or '{' or '(' or '"'
	// squash the value, ignoring all nested arrays and objects.
	var i, depth int
	if json[0] != '"' {
		i, depth = 1, 1
	}
	for ; i < len(json); i++ {
		if json[i] >= '"' && json[i] <= '}' {
			switch json[i] {
			case '"':
				i++
				s2 := i
				for ; i < len(json); i++ {
					if json[i] > '\\' {
						continue
					}
					if json[i] == '"' {
						// look for an escaped slash
						if json[i-1] == '\\' {
							n := 0
							for j := i - 2; j > s2-1; j-- {
								if json[j] != '\\' {
									break
								}
								n++
							}
							if n%2 == 0 {
								continue
							}
						}
						break
					}
				}
				if depth == 0 {
					if i >= len(json) {
						return json
					}
					return json[:i+1]
				}
			case '{', '[', '(':
				depth++
			case '}', ']', ')':
				depth--
				if depth == 0 {
					return json[:i+1]
				}
			}
		}
	}
	return json
}

func tonum(json string) (raw string, num float64) {
	for i := 1; i < len(json); i++ {
		// less than dash might have valid characters
		if json[i] <= '-' {
			if json[i] <= ' ' || json[i] == ',' {
				// break on whitespace and comma
				raw = json[:i]
				num, _ = strconv.ParseFloat(raw, 64)
				return
			}
			// could be a '+' or '-'. let's assume so.
		} else if json[i] == ']' || json[i] == '}' {
			// break on ']' or '}'
			raw = json[:i]
			num, _ = strconv.ParseFloat(raw, 64)
			return
		}
	}
	raw = json
	num, _ = strconv.ParseFloat(raw, 64)
	return
}

func tolit(json string) (raw string) {
	for i := 1; i < len(json); i++ {
		if json[i] < 'a' || json[i] > 'z' {
			return json[:i]
		}
	}
	return json
}

func tostr(json string) (raw string, str string) {
	// expects that the lead character is a '"'
	for i := 1; i < len(json); i++ {
		if json[i] > '\\' {
			continue
		}
		if json[i] == '"' {
			return json[:i+1], json[1:i]
		}
		if json[i] == '\\' {
			i++
			for ; i < len(json); i++ {
				if json[i] > '\\' {
					continue
				}
				if json[i] == '"' {
					// look for an escaped slash
					if json[i-1] == '\\' {
						n := 0
						for j := i - 2; j > 0; j-- {
							if json[j] != '\\' {
								break
							}
							n++
						}
						if n%2 == 0 {
							continue
						}
					}
					return json[:i+1], unescape(json[1:i])
				}
			}
			var ret string
			if i+1 < len(json) {
				ret = json[:i+1]
			} else {
				ret = json[:i]
			}
			return ret, unescape(json[1:i])
		}
	}
	return json, json[1:]
}

// Exists returns true if value exists.
//
//	 if gjson.Get(json, "name.last").Exists(){
//			println("value exists")
//	 }
func (t Result) Exists() bool {
	return t.Type != Null || len(t.Raw) != 0
}

// Value returns one of these types:
//
//	bool, for JSON booleans
//	float64, for JSON numbers
//	Number, for JSON numbers
//	string, for JSON string literals
//	nil, for JSON null
//	map[string]interface{}, for JSON objects
//	[]interface{}, for JSON arrays
func (t Result) Value() interface{} {
	if t.Type == String {
		return t.Str
	}
	switch t.Type {
	default:
		return nil
	case False:
		return false
	case Number:
		return t.Num
	case JSON:
		r := t.arrayOrMap(0, true)
		if r.vc == '{' {
			return r.oi
		} else if r.vc == '[' {
			return r.ai
		}
		return nil
	case True:
		return true
	}
}

func parseString(json string, i int) (int, string, bool, bool) {
	var s = i
	for ; i < len(json); i++ {
		if json[i] > '\\' {
			continue
		}
		if json[i] == '"' {
			return i + 1, json[s-1 : i+1], false, true
		}
		if json[i] == '\\' {
			i++
			for ; i < len(json); i++ {
				if json[i] > '\\' {
					continue
				}
				if json[i] == '"' {
					// look for an escaped slash
					if json[i-1] == '\\' {
						n := 0
						for j := i - 2; j > 0; j-- {
							if json[j] != '\\' {
								break
							}
							n++
						}
						if n%2 == 0 {
							continue
						}
					}
					return i + 1, json[s-1 : i+1], true, true
				}
			}
			break
		}
	}
	return i, json[s-1:], false, false
}

func parseNumber(json string, i int) (int, string) {
	var s = i
	i++
	for ; i < len(json); i++ {
		if json[i] <= ' ' || json[i] == ',' || json[i] == ']' ||
			json[i] == '}' {
			return i, json[s:i]
		}
	}
	return i, json[s:]
}

func parseLiteral(json string, i int) (int, string) {
	var s = i
	i++
	for ; i < len(json); i++ {
		if json[i] < 'a' || json[i] > 'z' {
			return i, json[s:i]
		}
	}
	return i, json[s:]
}

type arrayPathResult struct {
	part    string
	path    string
	pipe    string
	piped   bool
	more    bool
	alogok  bool
	arrch   bool
	alogkey string
	query   struct {
		on    bool
		all   bool
		path  string
		op    string
		value string
	}
}

func parseArrayPath(path string) (r arrayPathResult) {
	for i := 0; i < len(path); i++ {
		if path[i] == '|' {
			r.part = path[:i]
			r.pipe = path[i+1:]
			r.piped = true
			return
		}
		if path[i] == '.' {
			r.part = path[:i]
			if !r.arrch && i < len(path)-1 && isDotPiperChar(path[i+1:]) {
				r.pipe = path[i+1:]
				r.piped = true
			} else {
				r.path = path[i+1:]
				r.more = true
			}
			return
		}
		if path[i] == '#' {
			r.arrch = true
			if i == 0 && len(path) > 1 {
				if path[1] == '.' {
					r.alogok = true
					r.alogkey = path[2:]
					r.path = path[:1]
				} else if path[1] == '[' || path[1] == '(' {
					// query
					r.query.on = true
					qpath, op, value, _, fi, vesc, ok :=
						parseQuery(path[i:])
					if !ok {
						// bad query, end now
						break
					}
					if len(value) >= 2 && value[0] == '"' &&
						value[len(value)-1] == '"' {
						value = value[1 : len(value)-1]
						if vesc {
							value = unescape(value)
						}
					}
					r.query.path = qpath
					r.query.op = op
					r.query.value = value

					i = fi - 1
					if i+1 < len(path) && path[i+1] == '#' {
						r.query.all = true
					}
				}
			}
			continue
		}
	}
	r.part = path
	r.path = ""
	return
}

// splitQuery takes a query and splits it into three parts:
//
//	path, op, middle, and right.
//
// So for this query:
//
//	#(first_name=="Murphy").last
//
// Becomes
//
//	first_name   # path
//	=="Murphy"   # middle
//	.last        # right
//
// Or,
//
//	#(service_roles.#(=="one")).cap
//
// Becomes
//
//	service_roles.#(=="one")   # path
//	                           # middle
//	.cap                       # right
func parseQuery(query string) (
	path, op, value, remain string, i int, vesc, ok bool,
) {
	if len(query) < 2 || query[0] != '#' ||
		(query[1] != '(' && query[1] != '[') {
		return "", "", "", "", i, false, false
	}
	i = 2
	j := 0 // start of value part
	depth := 1
	for ; i < len(query); i++ {
		if depth == 1 && j == 0 {
			switch query[i] {
			case '!', '=', '<', '>', '%':
				// start of the value part
				j = i
				continue
			}
		}
		if query[i] == '\\' {
			i++
		} else if query[i] == '[' || query[i] == '(' {
			depth++
		} else if query[i] == ']' || query[i] == ')' {
			depth--
			if depth == 0 {
				break
			}
		} else if query[i] == '"' {
			// inside selector string, balance quotes
			i++
			for ; i < len(query); i++ {
				if query[i] == '\\' {
					vesc = true
					i++
				} else if query[i] == '"' {
					break
				}
			}
		}
	}
	if depth > 0 {
		return "", "", "", "", i, false, false
	}
	if j > 0 {
		path = trim(query[2:j])
		value = trim(query[j:i])
		remain = query[i+1:]
		// parse the compare op from the value
		var opsz int
		switch {
		case len(value) == 1:
			opsz = 1
		case value[0] == '!' && value[1] == '=':
			opsz = 2
		case value[0] == '!' && value[1] == '%':
			opsz = 2
		case value[0] == '<' && value[1] == '=':
			opsz = 2
		case value[0] == '>' && value[1] == '=':
			opsz = 2
		case value[0] == '=' && value[1] == '=':
			value = value[1:]
			opsz = 1
		case value[0] == '<':
			opsz = 1
		case value[0] == '>':
			opsz = 1
		case value[0] == '=':
			opsz = 1
		case value[0] == '%':
			opsz = 1
		}
		op = value[:opsz]
		value = trim(value[opsz:])
	} else {
		path = trim(query[2:i])
		remain = query[i+1:]
	}
	return path, op, value, remain, i + 1, vesc, true
}

func trim(s string) string {
left:
	if len(s) > 0 && s[0] <= ' ' {
		s = s[1:]
		goto left
	}
right:
	if len(s) > 0 && s[len(s)-1] <= ' ' {
		s = s[:len(s)-1]
		goto right
	}
	return s
}

// peek at the next byte and see if it's a '@', '[', or '{'.
func isDotPiperChar(s string) bool {
	if DisableModifiers {
		return false
	}
	c := s[0]
	if c == '@' {
		// check that the next component is *not* a modifier.
		i := 1
		for ; i < len(s); i++ {
			if s[i] == '.' || s[i] == '|' || s[i] == ':' {
				break
			}
		}
		_, ok := modifiers[s[1:i]]
		return ok
	}
	return c == '[' || c == '{'
}

type objectPathResult struct {
	part  string
	path  string
	pipe  string
	piped bool
	wild  bool
	more  bool
}

func parseObjectPath(path string) (r objectPathResult) {
	for i := 0; i < len(path); i++ {
		if path[i] == '|' {
			r.part = path[:i]
			r.pipe = path[i+1:]
			r.piped = true
			return
		}
		if path[i] == '.' {
			r.part = path[:i]
			if i < len(path)-1 && isDotPiperChar(path[i+1:]) {
				r.pipe = path[i+1:]
				r.piped = true
			} else {
				r.path = path[i+1:]
				r.more = true
			}
			return
		}
		if path[i] == '*' || path[i] == '?' {
			r.wild = true
			continue
		}
		if path[i] == '\\' {
			// go into escape mode. this is a slower path that
			// strips off the escape character from the part.
			epart := []byte(path[:i])
			i++
			if i < len(path) {
				epart = append(epart, path[i])
				i++
				for ; i < len(path); i++ {
					if path[i] == '\\' {
						i++
						if i < len(path) {
							epart = append(epart, path[i])
						}
						continue
					} else if path[i] == '.' {
						r.part = string(epart)
						if i < len(path)-1 && isDotPiperChar(path[i+1:]) {
							r.pipe = path[i+1:]
							r.piped = true
						} else {
							r.path = path[i+1:]
							r.more = true
						}
						return
					} else if path[i] == '|' {
						r.part = string(epart)
						r.pipe = path[i+1:]
						r.piped = true
						return
					} else if path[i] == '*' || path[i] == '?' {
						r.wild = true
					}
					epart = append(epart, path[i])
				}
			}
			// append the last part
			r.part = string(epart)
			return
		}
	}
	r.part = path
	return
}

var vchars = [256]byte{
	'"': 2, '{': 3, '(': 3, '[': 3, '}': 1, ')': 1, ']': 1,
}

func parseSquash(json string, i int) (int, string) {
	// expects that the lead character is a '[' or '{' or '('
	// squash the value, ignoring all nested arrays and objects.
	// the first '[' or '{' or '(' has already been read
	s := i
	i++
	depth := 1
	var c byte
	for i < len(json) {
		for i < len(json)-8 {
			jslice := json[i : i+8]
			c = vchars[jslice[0]]
			if c != 0 {
				i += 0
				goto token
			}
			c = vchars[jslice[1]]
			if c != 0 {
				i += 1
				goto token
			}
			c = vchars[jslice[2]]
			if c != 0 {
				i += 2
				goto token
			}
			c = vchars[jslice[3]]
			if c != 0 {
				i += 3
				goto token
			}
			c = vchars[jslice[4]]
			if c != 0 {
				i += 4
				goto token
			}
			c = vchars[jslice[5]]
			if c != 0 {
				i += 5
				goto token
			}
			c = vchars[jslice[6]]
			if c != 0 {
				i += 6
				goto token
			}
			c = vchars[jslice[7]]
			if c != 0 {
				i += 7
				goto token
			}
			i += 8
		}
		c = vchars[json[i]]
		if c == 0 {
			i++
			continue
		}
	token:
		if c == 2 {
			// '"' string
			i++
			s2 := i
		nextquote:
			for i < len(json)-8 {
				jslice := json[i : i+8]
				if jslice[0] == '"' {
					i += 0
					goto strchkesc
				}
				if jslice[1] == '"' {
					i += 1
					goto strchkesc
				}
				if jslice[2] == '"' {
					i += 2
					goto strchkesc
				}
				if jslice[3] == '"' {
					i += 3
					goto strchkesc
				}
				if jslice[4] == '"' {
					i += 4
					goto strchkesc
				}
				if jslice[5] == '"' {
					i += 5
					goto strchkesc
				}
				if jslice[6] == '"' {
					i += 6
					goto strchkesc
				}
				if jslice[7] == '"' {
					i += 7
					goto strchkesc
				}
				i += 8
			}
			goto strchkstd
		strchkesc:
			if json[i-1] != '\\' {
				i++
				continue
			}
		strchkstd:
			for i < len(json) {
				if json[i] > '\\' || json[i] != '"' {
					i++
					continue
				}
				// look for an escaped slash
				if json[i-1] == '\\' {
					n := 0
					for j := i - 2; j > s2-1; j-- {
						if json[j] != '\\' {
							break
						}
						n++
					}
					if n%2 == 0 {
						i++
						goto nextquote
					}
				}
				break
			}
		} else {
			// '{', '[', '(', '}', ']', ')'
			// open close tokens
			depth += int(c) - 2
			if depth == 0 {
				i++
				return i, json[s:i]
			}
		}
		i++
	}
	return i, json[s:]
}

func parseObject(c *parseContext, i int, path string) (int, bool) {
	var pmatch, kesc, vesc, ok, hit bool
	var key, val string
	rp := parseObjectPath(path)
	if !rp.more && rp.piped {
		c.pipe = rp.pipe
		c.piped = true
	}
	for i < len(c.json) {
		for ; i < len(c.json); i++ {
			if c.json[i] == '"' {
				// parse_key_string
				// this is slightly different from getting s string value
				// because we don't need the outer quotes.
				i++
				var s = i
				for ; i < len(c.json); i++ {
					if c.json[i] > '\\' {
						continue
					}
					if c.json[i] == '"' {
						i, key, kesc, ok = i+1, c.json[s:i], false, true
						goto parse_key_string_done
					}
					if c.json[i] == '\\' {
						i++
						for ; i < len(c.json); i++ {
							if c.json[i] > '\\' {
								continue
							}
							if c.json[i] == '"' {
								// look for an escaped slash
								if c.json[i-1] == '\\' {
									n := 0
									for j := i - 2; j > 0; j-- {
										if c.json[j] != '\\' {
											break
										}
										n++
									}
									if n%2 == 0 {
										continue
									}
								}
								i, key, kesc, ok = i+1, c.json[s:i], true, true
								goto parse_key_string_done
							}
						}
						break
					}
				}
				key, kesc, ok = c.json[s:], false, false
			parse_key_string_done:
				break
			}
			if c.json[i] == '}' {
				return i + 1, false
			}
		}
		if !ok {
			return i, false
		}
		if rp.wild {
			if kesc {
				pmatch = matchLimit(unescape(key), rp.part)
			} else {
				pmatch = matchLimit(key, rp.part)
			}
		} else {
			if kesc {
				pmatch = rp.part == unescape(key)
			} else {
				pmatch = rp.part == key
			}
		}
		hit = pmatch && !rp.more
		for ; i < len(c.json); i++ {
			var num bool
			switch c.json[i] {
			default:
				continue
			case '"':
				i++
				i, val, vesc, ok = parseString(c.json, i)
				if !ok {
					return i, false
				}
				if hit {
					if vesc {
						c.value.Str = unescape(val[1 : len(val)-1])
					} else {
						c.value.Str = val[1 : len(val)-1]
					}
					c.value.Raw = val
					c.value.Type = String
					return i, true
				}
			case '{':
				if pmatch && !hit {
					i, hit = parseObject(c, i+1, rp.path)
					if hit {
						return i, true
					}
				} else {
					i, val = parseSquash(c.json, i)
					if hit {
						c.value.Raw = val
						c.value.Type = JSON
						return i, true
					}
				}
			case '[':
				if pmatch && !hit {
					i, hit = parseArray(c, i+1, rp.path)
					if hit {
						return i, true
					}
				} else {
					i, val = parseSquash(c.json, i)
					if hit {
						c.value.Raw = val
						c.value.Type = JSON
						return i, true
					}
				}
			case 'n':
				if i+1 < len(c.json) && c.json[i+1] != 'u' {
					num = true
					break
				}
				fallthrough
			case 't', 'f':
				vc := c.json[i]
				i, val = parseLiteral(c.json, i)
				if hit {
					c.value.Raw = val
					switch vc {
					case 't':
						c.value.Type = True
					case 'f':
						c.value.Type = False
					}
					return i, true
				}
			case '+', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
				'i', 'I', 'N':
				num = true
			}
			if num {
				i, val = parseNumber(c.json, i)
				if hit {
					c.value.Raw = val
					c.value.Type = Number
					c.value.Num, _ = strconv.ParseFloat(val, 64)
					return i, true
				}
			}
			break
		}
	}
	return i, false
}

// matchLimit will limit the complexity of the match operation to avoid ReDos
// attacks from arbitrary inputs.
// See the github.com/tidwall/match.MatchLimit function for more information.
func matchLimit(str, pattern string) bool {
	matched, _ := match.MatchLimit(str, pattern, 10000)
	return matched
}

func falseish(t Result) bool {
	switch t.Type {
	case Null:
		return true
	case False:
		return true
	case String:
		b, err := strconv.ParseBool(strings.ToLower(t.Str))
		if err != nil {
			return false
		}
		return !b
	case Number:
		return t.Num == 0
	default:
		return false
	}
}

func trueish(t Result) bool {
	switch t.Type {
	case True:
		return true
	case String:
		b, err := strconv.ParseBool(strings.ToLower(t.Str))
		if err != nil {
			return false
		}
		return b
	case Number:
		return t.Num != 0
	default:
		return false
	}
}

func nullish(t Result) bool {
	return t.Type == Null
}

func queryMatches(rp *arrayPathResult, value Result) bool {
	rpv := rp.query.value
	if len(rpv) > 0 {
		if rpv[0] == '~' {
			// convert to bool
			rpv = rpv[1:]
			var ish, ok bool
			switch rpv {
			case "*":
				ish, ok = value.Exists(), true
			case "null":
				ish, ok = nullish(value), true
			case "true":
				ish, ok = trueish(value), true
			case "false":
				ish, ok = falseish(value), true
			}
			if ok {
				rpv = "true"
				if ish {
					value = Result{Type: True}
				} else {
					value = Result{Type: False}
				}
			} else {
				rpv = ""
				value = Result{}
			}
		}
	}
	if !value.Exists() {
		return false
	}
	if rp.query.op == "" {
		// the query is only looking for existence, such as:
		//   friends.#(name)
		// which makes sure that the array "friends" has an element of
		// "name" that exists
		return true
	}
	switch value.Type {
	case String:
		switch rp.query.op {
		case "=":
			return value.Str == rpv
		case "!=":
			return value.Str != rpv
		case "<":
			return value.Str < rpv
		case "<=":
			return value.Str <= rpv
		case ">":
			return value.Str > rpv
		case ">=":
			return value.Str >= rpv
		case "%":
			return matchLimit(value.Str, rpv)
		case "!%":
			return !matchLimit(value.Str, rpv)
		}
	case Number:
		rpvn, _ := strconv.ParseFloat(rpv, 64)
		switch rp.query.op {
		case "=":
			return value.Num == rpvn
		case "!=":
			return value.Num != rpvn
		case "<":
			return value.Num < rpvn
		case "<=":
			return value.Num <= rpvn
		case ">":
			return value.Num > rpvn
		case ">=":
			return value.Num >= rpvn
		}
	case True:
		switch rp.query.op {
		case "=":
			return rpv == "true"
		case "!=":
			return rpv != "true"
		case ">":
			return rpv == "false"
		case ">=":
			return true
		}
	case False:
		switch rp.query.op {
		case "=":
			return rpv == "false"
		case "!=":
			return rpv != "false"
		case "<":
			return rpv == "true"
		case "<=":
			return true
		}
	}
	return false
}
func parseArray(c *parseContext, i int, path string) (int, bool) {
	var pmatch, vesc, ok, hit bool
	var val string
	var h int
	var alog []int
	var partidx int
	var multires []byte
	var queryIndexes []int
	rp := parseArrayPath(path)
	if !rp.arrch {
		n, ok := parseUint(rp.part)
		if !ok {
			partidx = -1
		} else {
			partidx = int(n)
		}
	}
	if !rp.more && rp.piped {
		c.pipe = rp.pipe
		c.piped = true
	}

	procQuery := func(qval Result) bool {
		if rp.query.all {
			if len(multires) == 0 {
				multires = append(multires, '[')
			}
		}
		var tmp parseContext
		tmp.value = qval
		fillIndex(c.json, &tmp)
		parentIndex := tmp.value.Index
		var res Result
		if qval.Type == JSON {
			res = qval.Get(rp.query.path)
		} else {
			if rp.query.path != "" {
				return false
			}
			res = qval
		}
		if queryMatches(&rp, res) {
			if rp.more {
				left, right, ok := splitPossiblePipe(rp.path)
				if ok {
					rp.path = left
					c.pipe = right
					c.piped = true
				}
				res = qval.Get(rp.path)
			} else {
				res = qval
			}
			if rp.query.all {
				raw := res.Raw
				if len(raw) == 0 {
					raw = res.String()
				}
				if raw != "" {
					if len(multires) > 1 {
						multires = append(multires, ',')
					}
					multires = append(multires, raw...)
					queryIndexes = append(queryIndexes, res.Index+parentIndex)
				}
			} else {
				c.value = res
				return true
			}
		}
		return false
	}
	for i < len(c.json)+1 {
		if !rp.arrch {
			pmatch = partidx == h
			hit = pmatch && !rp.more
		}
		h++
		if rp.alogok {
			alog = append(alog, i)
		}
		for ; ; i++ {
			var ch byte
			if i > len(c.json) {
				break
			} else if i == len(c.json) {
				ch = ']'
			} else {
				ch = c.json[i]
			}
			var num bool
			switch ch {
			default:
				continue
			case '"':
				i++
				i, val, vesc, ok = parseString(c.json, i)
				if !ok {
					return i, false
				}
				if rp.query.on {
					var qval Result
					if vesc {
						qval.Str = unescape(val[1 : len(val)-1])
					} else {
						qval.Str = val[1 : len(val)-1]
					}
					qval.Raw = val
					qval.Type = String
					if procQuery(qval) {
						return i, true
					}
				} else if hit {
					if rp.alogok {
						break
					}
					if vesc {
						c.value.Str = unescape(val[1 : len(val)-1])
					} else {
						c.value.Str = val[1 : len(val)-1]
					}
					c.value.Raw = val
					c.value.Type = String
					return i, true
				}
			case '{':
				if pmatch && !hit {
					i, hit = parseObject(c, i+1, rp.path)
					if hit {
						if rp.alogok {
							break
						}
						return i, true
					}
				} else {
					i, val = parseSquash(c.json, i)
					if rp.query.on {
						if procQuery(Result{Raw: val, Type: JSON}) {
							return i, true
						}
					} else if hit {
						if rp.alogok {
							break
						}
						c.value.Raw = val
						c.value.Type = JSON
						return i, true
					}
				}
			case '[':
				if pmatch && !hit {
					i, hit = parseArray(c, i+1, rp.path)
					if hit {
						if rp.alogok {
							break
						}
						return i, true
					}
				} else {
					i, val = parseSquash(c.json, i)
					if rp.query.on {
						if procQuery(Result{Raw: val, Type: JSON}) {
							return i, true
						}
					} else if hit {
						if rp.alogok {
							break
						}
						c.value.Raw = val
						c.value.Type = JSON
						return i, true
					}
				}
			case 'n':
				if i+1 < len(c.json) && c.json[i+1] != 'u' {
					num = true
					break
				}
				fallthrough
			case 't', 'f':
				vc := c.json[i]
				i, val = parseLiteral(c.json, i)
				if rp.query.on {
					var qval Result
					qval.Raw = val
					switch vc {
					case 't':
						qval.Type = True
					case 'f':
						qval.Type = False
					}
					if procQuery(qval) {
						return i, true
					}
				} else if hit {
					if rp.alogok {
						break
					}
					c.value.Raw = val
					switch vc {
					case 't':
						c.value.Type = True
					case 'f':
						c.value.Type = False
					}
					return i, true
				}
			case '+', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
				'i', 'I', 'N':
				num = true
			case ']':
				if rp.arrch && rp.part == "#" {
					if rp.alogok {
						left, right, ok := splitPossiblePipe(rp.alogkey)
						if ok {
							rp.alogkey = left
							c.pipe = right
							c.piped = true
						}
						var indexes = make([]int, 0, 64)
						var jsons = make([]byte, 0, 64)
						jsons = append(jsons, '[')
						for j, k := 0, 0; j < len(alog); j++ {
							idx := alog[j]
							for idx < len(c.json) {
								switch c.json[idx] {
								case ' ', '\t', '\r', '\n':
									idx++
									continue
								}
								break
							}
							if idx < len(c.json) && c.json[idx] != ']' {
								_, res, ok := parseAny(c.json, idx, true)
								if ok {
									res := res.Get(rp.alogkey)
									if res.Exists() {
										if k > 0 {
											jsons = append(jsons, ',')
										}
										raw := res.Raw
										if len(raw) == 0 {
											raw = res.String()
										}
										jsons = append(jsons, []byte(raw)...)
										indexes = append(indexes, res.Index)
										k++
									}
								}
							}
						}
						jsons = append(jsons, ']')
						c.value.Type = JSON
						c.value.Raw = string(jsons)
						c.value.Indexes = indexes
						return i + 1, true
					}
					if rp.alogok {
						break
					}

					c.value.Type = Number
					c.value.Num = float64(h - 1)
					c.value.Raw = strconv.Itoa(h - 1)
					c.calcd = true
					return i + 1, true
				}
				if !c.value.Exists() {
					if len(multires) > 0 {
						c.value = Result{
							Raw:     string(append(multires, ']')),
							Type:    JSON,
							Indexes: queryIndexes,
						}
					} else if rp.query.all {
						c.value = Result{
							Raw:  "[]",
							Type: JSON,
						}
					}
				}
				return i + 1, false
			}
			if num {
				i, val = parseNumber(c.json, i)
				if rp.query.on {
					var qval Result
					qval.Raw = val
					qval.Type = Number
					qval.Num, _ = strconv.ParseFloat(val, 64)
					if procQuery(qval) {
						return i, true
					}
				} else if hit {
					if rp.alogok {
						break
					}
					c.value.Raw = val
					c.value.Type = Number
					c.value.Num, _ = strconv.ParseFloat(val, 64)
					return i, true
				}
			}
			break
		}
	}
	return i, false
}

func splitPossiblePipe(path string) (left, right string, ok bool) {
	// take a quick peek for the pipe character. If found we'll split the piped
	// part of the path into the c.pipe field and shorten the rp.
	var possible bool
	for i := 0; i < len(path); i++ {
		if path[i] == '|' {
			possible = true
			break
		}
	}
	if !possible {
		return
	}

	if len(path) > 0 && path[0] == '{' {
		squashed := squash(path[1:])
		if len(squashed) < len(path)-1 {
			squashed = path[:len(squashed)+1]
			remain := path[len(squashed):]
			if remain[0] == '|' {
				return squashed, remain[1:], true
			}
		}
		return
	}

	// split the left and right side of the path with the pipe character as
	// the delimiter. This is a little tricky because we'll need to basically
	// parse the entire path.
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' {
			i++
		} else if path[i] == '.' {
			if i == len(path)-1 {
				return
			}
			if path[i+1] == '#' {
				i += 2
				if i == len(path) {
					return
				}
				if path[i] == '[' || path[i] == '(' {
					var start, end byte
					if path[i] == '[' {
						start, end = '[', ']'
					} else {
						start, end = '(', ')'
					}
					// inside selector, balance brackets
					i++
					depth := 1
					for ; i < len(path); i++ {
						if path[i] == '\\' {
							i++
						} else if path[i] == start {
							depth++
						} else if path[i] == end {
							depth--
							if depth == 0 {
								break
							}
						} else if path[i] == '"' {
							// inside selector string, balance quotes
							i++
							for ; i < len(path); i++ {
								if path[i] == '\\' {
									i++
								} else if path[i] == '"' {
									break
								}
							}
						}
					}
				}
			}
		} else if path[i] == '|' {
			return path[:i], path[i+1:], true
		}
	}
	return
}

// ForEachLine iterates through lines of JSON as specified by the JSON Lines
// format (http://jsonlines.org/).
// Each line is returned as a GJSON Result.
func ForEachLine(json string, iterator func(line Result) bool) {
	var res Result
	var i int
	for {
		i, res, _ = parseAny(json, i, true)
		if !res.Exists() {
			break
		}
		if !iterator(res) {
			return
		}
	}
}

type subSelector struct {
	name string
	path string
}

// parseSubSelectors returns the subselectors belonging to a '[path1,path2]' or
// '{"field1":path1,"field2":path2}' type subSelection. It's expected that the
// first character in path is either '[' or '{', and has already been checked
// prior to calling this function.
func parseSubSelectors(path string) (sels []subSelector, out string, ok bool) {
	modifier := 0
	depth := 1
	colon := 0
	start := 1
	i := 1
	pushSel := func() {
		var sel subSelector
		if colon == 0 {
			sel.path = path[start:i]
		} else {
			sel.name = path[start:colon]
			sel.path = path[colon+1 : i]
		}
		sels = append(sels, sel)
		colon = 0
		modifier = 0
		start = i + 1
	}
	for ; i < len(path); i++ {
		switch path[i] {
		case '\\':
			i++
		case '@':
			if modifier == 0 && i > 0 && (path[i-1] == '.' || path[i-1] == '|') {
				modifier = i
			}
		case ':':
			if modifier == 0 && colon == 0 && depth == 1 {
				colon = i
			}
		case ',':
			if depth == 1 {
				pushSel()
			}
		case '"':
			i++
		loop:
			for ; i < len(path); i++ {
				switch path[i] {
				case '\\':
					i++
				case '"':
					break loop
				}
			}
		case '[', '(', '{':
			depth++
		case ']', ')', '}':
			depth--
			if depth == 0 {
				pushSel()
				path = path[i+1:]
				return sels, path, true
			}
		}
	}
	return
}

// nameOfLast returns the name of the last component
func nameOfLast(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '|' || path[i] == '.' {
			if i > 0 {
				if path[i-1] == '\\' {
					continue
				}
			}
			return path[i+1:]
		}
	}
	return path
}

func isSimpleName(component string) bool {
	for i := 0; i < len(component); i++ {
		if component[i] < ' ' {
			return false
		}
		switch component[i] {
		case '[', ']', '{', '}', '(', ')', '#', '|', '!':
			return false
		}
	}
	return true
}

var hexchars = [...]byte{
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
	'a', 'b', 'c', 'd', 'e', 'f',
}

func appendHex16(dst []byte, x uint16) []byte {
	return append(dst,
		hexchars[x>>12&0xF], hexchars[x>>8&0xF],
		hexchars[x>>4&0xF], hexchars[x>>0&0xF],
	)
}

// DisableEscapeHTML will disable the automatic escaping of certain
// "problamatic" HTML characters when encoding to JSON.
// These character include '>', '<' and '&', which get escaped to \u003e,
// \u0026, and \u003c respectively.
//
// This is a global flag and will affect all further gjson operations.
// Ideally, if used, it should be set one time before other gjson functions
// are called.
var DisableEscapeHTML = false

// AppendJSONString is a convenience function that converts the provided string
// to a valid JSON string and appends it to dst.
func AppendJSONString(dst []byte, s string) []byte {
	dst = append(dst, make([]byte, len(s)+2)...)
	dst = append(dst[:len(dst)-len(s)-2], '"')
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' {
			dst = append(dst, '\\')
			switch s[i] {
			case '\b':
				dst = append(dst, 'b')
			case '\f':
				dst = append(dst, 'f')
			case '\n':
				dst = append(dst, 'n')
			case '\r':
				dst = append(dst, 'r')
			case '\t':
				dst = append(dst, 't')
			default:
				dst = append(dst, 'u')
				dst = appendHex16(dst, uint16(s[i]))
			}
		} else if !DisableEscapeHTML &&
			(s[i] == '>' || s[i] == '<' || s[i] == '&') {
			dst = append(dst, '\\', 'u')
			dst = appendHex16(dst, uint16(s[i]))
		} else if s[i] == '\\' {
			dst = append(dst, '\\', '\\')
		} else if s[i] == '"' {
			dst = append(dst, '\\', '"')
		} else if s[i] > 127 {
			// read utf8 character
			r, n := utf8.DecodeRuneInString(s[i:])
			if n == 0 {
				break
			}
			if r == utf8.RuneError && n == 1 {
				dst = append(dst, `\ufffd`...)
			} else if r == '\u2028' || r == '\u2029' {
				dst = append(dst, `\u202`...)
				dst = append(dst, hexchars[r&0xF])
			} else {
				dst = append(dst, s[i:i+n]...)
			}
			i = i + n - 1
		} else {
			dst = append(dst, s[i])
		}
	}
	return append(dst, '"')
}

type parseContext struct {
	json  string
	value Result
	pipe  string
	piped bool
	calcd bool
	lines bool
}

// Get searches json for the specified path.
// A path is in dot syntax, such as "name.last" or "age".
// When the value is found it's returned immediately.
//
// A path is a series of keys separated by a dot.
// A key may contain special wildcard characters '*' and '?'.
// To access an array value use the index as the key.
// To get the number of elements in an array or to access a child path, use
// the '#' character.
// The dot and wildcard character can be escaped with '\'.
//
//	{
//	  "name": {"first": "Tom", "last": "Anderson"},
//	  "age":37,
//	  "children": ["Sara","Alex","Jack"],
//	  "friends": [
//	    {"first": "James", "last": "Murphy"},
//	    {"first": "Roger", "last": "Craig"}
//	  ]
//	}
//	"name.last"          >> "Anderson"
//	"age"                >> 37
//	"children"           >> ["Sara","Alex","Jack"]
//	"children.#"         >> 3
//	"children.1"         >> "Alex"
//	"child*.2"           >> "Jack"
//	"c?ildren.0"         >> "Sara"
//	"friends.#.first"    >> ["James","Roger"]
//
// This function expects that the json is well-formed, and does not validate.
// Invalid json will not panic, but it may return back unexpected results.
// If you are consuming JSON from an unpredictable source then you may want to
// use the Valid function first.
func Get(json, path string) Result {
	if len(path) > 1 {
		if (path[0] == '@' && !DisableModifiers) || path[0] == '!' {
			// possible modifier
			var ok bool
			var npath string
			var rjson string
			if path[0] == '@' && !DisableModifiers {
				npath, rjson, ok = execModifier(json, path)
			} else if path[0] == '!' {
				npath, rjson, ok = execStatic(json, path)
			}
			if ok {
				path = npath
				if len(path) > 0 && (path[0] == '|' || path[0] == '.') {
					res := Get(rjson, path[1:])
					res.Index = 0
					res.Indexes = nil
					return res
				}
				return Parse(rjson)
			}
		}
		if path[0] == '[' || path[0] == '{' {
			// using a subselector path
			kind := path[0]
			var ok bool
			var subs []subSelector
			subs, path, ok = parseSubSelectors(path)
			if ok {
				if len(path) == 0 || (path[0] == '|' || path[0] == '.') {
					var b []byte
					b = append(b, kind)
					var i int
					for _, sub := range subs {
						res := Get(json, sub.path)
						if res.Exists() {
							if i > 0 {
								b = append(b, ',')
							}
							if kind == '{' {
								if len(sub.name) > 0 {
									if sub.name[0] == '"' && Valid(sub.name) {
										b = append(b, sub.name...)
									} else {
										b = AppendJSONString(b, sub.name)
									}
								} else {
									last := nameOfLast(sub.path)
									if isSimpleName(last) {
										b = AppendJSONString(b, last)
									} else {
										b = AppendJSONString(b, "_")
									}
								}
								b = append(b, ':')
							}
							var raw string
							if len(res.Raw) == 0 {
								raw = res.String()
								if len(raw) == 0 {
									raw = "null"
								}
							} else {
								raw = res.Raw
							}
							b = append(b, raw...)
							i++
						}
					}
					b = append(b, kind+2)
					var res Result
					res.Raw = string(b)
					res.Type = JSON
					if len(path) > 0 {
						res = res.Get(path[1:])
					}
					res.Index = 0
					return res
				}
			}
		}
	}
	var i int
	var c = &parseContext{json: json}
	if len(path) >= 2 && path[0] == '.' && path[1] == '.' {
		c.lines = true
		parseArray(c, 0, path[2:])
	} else {
		for ; i < len(c.json); i++ {
			if c.json[i] == '{' {
				i++
				parseObject(c, i, path)
				break
			}
			if c.json[i] == '[' {
				i++
				parseArray(c, i, path)
				break
			}
		}
	}
	if c.piped {
		res := c.value.Get(c.pipe)
		res.Index = 0
		return res
	}
	fillIndex(json, c)
	return c.value
}

// GetBytes searches json for the specified path.
// If working with bytes, this method preferred over Get(string(data), path)
func GetBytes(json []byte, path string) Result {
	return getBytes(json, path)
}

// runeit returns the rune from the the \uXXXX
func runeit(json string) rune {
	n, _ := strconv.ParseUint(json[:4], 16, 64)
	return rune(n)
}

// unescape unescapes a string
func unescape(json string) string {
	var str = make([]byte, 0, len(json))
	for i := 0; i < len(json); i++ {
		switch {
		default:
			str = append(str, json[i])
		case json[i] < ' ':
			return string(str)
		case json[i] == '\\':
			i++
			if i >= len(json) {
				return string(str)
			}
			switch json[i] {
			default:
				return string(str)
			case '\\':
				str = append(str, '\\')
			case '/':
				str = append(str, '/')
			case 'b':
				str = append(str, '\b')
			case 'f':
				str = append(str, '\f')
			case 'n':
				str = append(str, '\n')
			case 'r':
				str = append(str, '\r')
			case 't':
				str = append(str, '\t')
			case '"':
				str = append(str, '"')
			case 'u':
				if i+5 > len(json) {
					return string(str)
				}
				r := runeit(json[i+1:])
				i += 5
				if utf16.IsSurrogate(r) {
					// need another code
					if len(json[i:]) >= 6 && json[i] == '\\' &&
						json[i+1] == 'u' {
						// we expect it to be correct so just consume it
						r = utf16.DecodeRune(r, runeit(json[i+2:]))
						i += 6
					}
				}
				// provide enough space to encode the largest utf8 possible
				str = append(str, 0, 0, 0, 0, 0, 0, 0, 0)
				n := utf8.EncodeRune(str[len(str)-8:], r)
				str = str[:len(str)-8+n]
				i-- // backtrack index by one
			}
		}
	}
	return string(str)
}

// Less return true if a token is less than another token.
// The caseSensitive parameter is used when the tokens are Strings.
// The order when comparing two different type is:
//
//	Null < False < Number < String < True < JSON
func (t Result) Less(token Result, caseSensitive bool) bool {
	if t.Type < token.Type {
		return true
	}
	if t.Type > token.Type {
		return false
	}
	if t.Type == String {
		if caseSensitive {
			return t.Str < token.Str
		}
		return stringLessInsensitive(t.Str, token.Str)
	}
	if t.Type == Number {
		return t.Num < token.Num
	}
	return t.Raw < token.Raw
}

func stringLessInsensitive(a, b string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] >= 'A' && a[i] <= 'Z' {
			if b[i] >= 'A' && b[i] <= 'Z' {
				// both are uppercase, do nothing
				if a[i] < b[i] {
					return true
				} else if a[i] > b[i] {
					return false
				}
			} else {
				// a is uppercase, convert a to lowercase
				if a[i]+32 < b[i] {
					return true
				} else if a[i]+32 > b[i] {
					return false
				}
			}
		} else if b[i] >= 'A' && b[i] <= 'Z' {
			// b is uppercase, convert b to lowercase
			if a[i] < b[i]+32 {
				return true
			} else if a[i] > b[i]+32 {
				return false
			}
		} else {
			// neither are uppercase
			if a[i] < b[i] {
				return true
			} else if a[i] > b[i] {
				return false
			}
		}
	}
	return len(a) < len(b)
}

// parseAny parses the next value from a json string.
// A Result is returned when the hit param is set.
// The return values are (i int, res Result, ok bool)
func parseAny(json string, i int, hit bool) (int, Result, bool) {
	var res Result
	var val string
	for ; i < len(json); i++ {
		if json[i] == '{' || json[i] == '[' {
			i, val = parseSquash(json, i)
			if hit {
				res.Raw = val
				res.Type = JSON
			}
			var tmp parseContext
			tmp.value = res
			fillIndex(json, &tmp)
			return i, tmp.value, true
		}
		if json[i] <= ' ' {
			continue
		}
		var num bool
		switch json[i] {
		case '"':
			i++
			var vesc bool
			var ok bool
			i, val, vesc, ok = parseString(json, i)
			if !ok {
				return i, res, false
			}
			if hit {
				res.Type = String
				res.Raw = val
				if vesc {
					res.Str = unescape(val[1 : len(val)-1])
				} else {
					res.Str = val[1 : len(val)-1]
				}
			}
			return i, res, true
		case 'n':
			if i+1 < len(json) && json[i+1] != 'u' {
				num = true
				break
			}
			fallthrough
		case 't', 'f':
			vc := json[i]
			i, val = parseLiteral(json, i)
			if hit {
				res.Raw = val
				switch vc {
				case 't':
					res.Type = True
				case 'f':
					res.Type = False
				}
				return i, res, true
			}
		case '+', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
			'i', 'I', 'N':
			num = true
		}
		if num {
			i, val = parseNumber(json, i)
			if hit {
				res.Raw = val
				res.Type = Number
				res.Num, _ = strconv.ParseFloat(val, 64)
			}
			return i, res, true
		}

	}
	return i, res, false
}

// GetMany searches json for the multiple paths.
// The return value is a Result array where the number of items
// will be equal to the number of input paths.
func GetMany(json string, path ...string) []Result {
	res := make([]Result, len(path))
	for i, path := range path {
		res[i] = Get(json, path)
	}
	return res
}

// GetManyBytes searches json for the multiple paths.
// The return value is a Result array where the number of items
// will be equal to the number of input paths.
func GetManyBytes(json []byte, path ...string) []Result {
	res := make([]Result, len(path))
	for i, path := range path {
		res[i] = GetBytes(json, path)
	}
	return res
}

func validpayload(data []byte, i int) (outi int, ok bool) {
	for ; i < len(data); i++ {
		switch data[i] {
		default:
			i, ok = validany(data, i)
			if !ok {
				return i, false
			}
			for ; i < len(data); i++ {
				switch data[i] {
				default:
					return i, false
				case ' ', '\t', '\n', '\r':
					continue
				}
			}
			return i, true
		case ' ', '\t', '\n', '\r':
			continue
		}
	}
	return i, false
}
func validany(data []byte, i int) (outi int, ok bool) {
	for ; i < len(data); i++ {
		switch data[i] {
		default:
			return i, false
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return validobject(data, i+1)
		case '[':
			return validarray(data, i+1)
		case '"':
			return validstring(data, i+1)
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return validnumber(data, i+1)
		case 't':
			return validtrue(data, i+1)
		case 'f':
			return validfalse(data, i+1)
		case 'n':
			return validnull(data, i+1)
		}
	}
	return i, false
}
func validobject(data []byte, i int) (outi int, ok bool) {
	for ; i < len(data); i++ {
		switch data[i] {
		default:
			return i, false
		case ' ', '\t', '\n', '\r':
			continue
		case '}':
			return i + 1, true
		case '"':
		key:
			if i, ok = validstring(data, i+1); !ok {
				return i, false
			}
			if i, ok = validcolon(data, i); !ok {
				return i, false
			}
			if i, ok = validany(data, i); !ok {
				return i, false
			}
			if i, ok = validcomma(data, i, '}'); !ok {
				return i, false
			}
			if data[i] == '}' {
				return i + 1, true
			}
			i++
			for ; i < len(data); i++ {
				switch data[i] {
				default:
					return i, false
				case ' ', '\t', '\n', '\r':
					continue
				case '"':
					goto key
				}
			}
			return i, false
		}
	}
	return i, false
}
func validcolon(data []byte, i int) (outi int, ok bool) {
	for ; i < len(data); i++ {
		switch data[i] {
		default:
			return i, false
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return i + 1, true
		}
	}
	return i, false
}
func validcomma(data []byte, i int, end byte) (outi int, ok bool) {
	for ; i < len(data); i++ {
		switch data[i] {
		default:
			return i, false
		case ' ', '\t', '\n', '\r':
			continue
		case ',':
			return i, true
		case end:
			return i, true
		}
	}
	return i, false
}
func validarray(data []byte, i int) (outi int, ok bool) {
	for ; i < len(data); i++ {
		switch data[i] {
		default:
			for ; i < len(data); i++ {
				if i, ok = validany(data, i); !ok {
					return i, false
				}
				if i, ok = validcomma(data, i, ']'); !ok {
					return i, false
				}
				if data[i] == ']' {
					return i + 1, true
				}
			}
		case ' ', '\t', '\n', '\r':
			continue
		case ']':
			return i + 1, true
		}
	}
	return i, false
}
func validstring(data []byte, i int) (outi int, ok bool) {
	for ; i < len(data); i++ {
		if data[i] < ' ' {
			return i, false
		} else if data[i] == '\\' {
			i++
			if i == len(data) {
				return i, false
			}
			switch data[i] {
			default:
				return i, false
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				for j := 0; j < 4; j++ {
					i++
					if i >= len(data) {
						return i, false
					}
					if !((data[i] >= '0' && data[i] <= '9') ||
						(data[i] >= 'a' && data[i] <= 'f') ||
						(data[i] >= 'A' && data[i] <= 'F')) {
						return i, false
					}
				}
			}
		} else if data[i] == '"' {
			return i + 1, true
		}
	}
	return i, false
}
func validnumber(data []byte, i int) (outi int, ok bool) {
	i--
	// sign
	if data[i] == '-' {
		i++
		if i == len(data) {
			return i, false
		}
		if data[i] < '0' || data[i] > '9' {
			return i, false
		}
	}
	// int
	if i == len(data) {
		return i, false
	}
	if data[i] == '0' {
		i++
	} else {
		for ; i < len(data); i++ {
			if data[i] >= '0' && data[i] <= '9' {
				continue
			}
			break
		}
	}
	// frac
	if i == len(data) {
		return i, true
	}
	if data[i] == '.' {
		i++
		if i == len(data) {
			return i, false
		}
		if data[i] < '0' || data[i] > '9' {
			return i, false
		}
		i++
		for ; i < len(data); i++ {
			if data[i] >= '0' && data[i] <= '9' {
				continue
			}
			break
		}
	}
	// exp
	if i == len(data) {
		return i, true
	}
	if data[i] == 'e' || data[i] == 'E' {
		i++
		if i == len(data) {
			return i, false
		}
		if data[i] == '+' || data[i] == '-' {
			i++
		}
		if i == len(data) {
			return i, false
		}
		if data[i] < '0' || data[i] > '9' {
			return i, false
		}
		i++
		for ; i < len(data); i++ {
			if data[i] >= '0' && data[i] <= '9' {
				continue
			}
			break
		}
	}
	return i, true
}

func validtrue(data []byte, i int) (outi int, ok bool) {
	if i+3 <= len(data) && data[i] == 'r' && data[i+1] == 'u' &&
		data[i+2] == 'e' {
		return i + 3, true
	}
	return i, false
}
func validfalse(data []byte, i int) (outi int, ok bool) {
	if i+4 <= len(data) && data[i] == 'a' && data[i+1] == 'l' &&
		data[i+2] == 's' && data[i+3] == 'e' {
		return i + 4, true
	}
	return i, false
}
func validnull(data []byte, i int) (outi int, ok bool) {
	if i+3 <= len(data) && data[i] == 'u' && data[i+1] == 'l' &&
		data[i+2] == 'l' {
		return i + 3, true
	}
	return i, false
}

// Valid returns true if the input is valid json.
//
//	if !gjson.Valid(json) {
//		return errors.New("invalid json")
//	}
//	value := gjson.Get(json, "name.last")
func Valid(json string) bool {
	_, ok := validpayload(stringBytes(json), 0)
	return ok
}

// ValidBytes returns true if the input is valid json.
//
//	if !gjson.Valid(json) {
//		return errors.New("invalid json")
//	}
//	value := gjson.Get(json, "name.last")
//
// If working with bytes, this method preferred over ValidBytes(string(data))
func ValidBytes(json []byte) bool {
	_, ok := validpayload(json, 0)
	return ok
}

func parseUint(s string) (n uint64, ok bool) {
	var i int
	if i == len(s) {
		return 0, false
	}
	for ; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + uint64(s[i]-'0')
		} else {
			return 0, false
		}
	}
	return n, true
}

func parseInt(s string) (n int64, ok bool) {
	var i int
	var sign bool
	if len(s) > 0 && s[0] == '-' {
		sign = true
		i++
	}
	if i == len(s) {
		return 0, false
	}
	for ; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int64(s[i]-'0')
		} else {
			return 0, false
		}
	}
	if sign {
		return n * -1, true
	}
	return n, true
}

// safeInt validates a given JSON number
// ensures it lies within the minimum and maximum representable JSON numbers
func safeInt(f float64) (n int64, ok bool) {
	// https://tc39.es/ecma262/#sec-number.min_safe_integer
	// https://tc39.es/ecma262/#sec-number.max_safe_integer
	if f < -9007199254740991 || f > 9007199254740991 {
		return 0, false
	}
	return int64(f), true
}

// execStatic parses the path to find a static value.
// The input expects that the path already starts with a '!'
func execStatic(json, path string) (pathOut, res string, ok bool) {
	name := path[1:]
	if len(name) > 0 {
		switch name[0] {
		case '{', '[', '"', '+', '-', '0', '1', '2', '3', '4', '5', '6', '7',
			'8', '9':
			_, res = parseSquash(name, 0)
			pathOut = name[len(res):]
			return pathOut, res, true
		}
	}
	for i := 1; i < len(path); i++ {
		if path[i] == '|' {
			pathOut = path[i:]
			name = path[1:i]
			break
		}
		if path[i] == '.' {
			pathOut = path[i:]
			name = path[1:i]
			break
		}
	}
	switch strings.ToLower(name) {
	case "true", "false", "null", "nan", "inf":
		return pathOut, name, true
	}
	return pathOut, res, false
}

// execModifier parses the path to find a matching modifier function.
// The input expects that the path already starts with a '@'
func execModifier(json, path string) (pathOut, res string, ok bool) {
	name := path[1:]
	var hasArgs bool
	for i := 1; i < len(path); i++ {
		if path[i] == ':' {
			pathOut = path[i+1:]
			name = path[1:i]
			hasArgs = len(pathOut) > 0
			break
		}
		if path[i] == '|' {
			pathOut = path[i:]
			name = path[1:i]
			break
		}
		if path[i] == '.' {
			pathOut = path[i:]
			name = path[1:i]
			break
		}
	}
	if fn, ok := modifiers[name]; ok {
		var args string
		if hasArgs {
			var parsedArgs bool
			switch pathOut[0] {
			case '{', '[', '"':
				// json arg
				res := Parse(pathOut)
				if res.Exists() {
					args = squash(pathOut)
					pathOut = pathOut[len(args):]
					parsedArgs = true
				}
			}
			if !parsedArgs {
				// simple arg
				i := 0
				for ; i < len(pathOut); i++ {
					if pathOut[i] == '|' {
						break
					}
					switch pathOut[i] {
					case '{', '[', '"', '(':
						s := squash(pathOut[i:])
						i += len(s) - 1
					}
				}
				args = pathOut[:i]
				pathOut = pathOut[i:]
			}
		}
		return pathOut, fn(json, args), true
	}
	return pathOut, res, false
}

// unwrap removes the '[]' or '{}' characters around json
func unwrap(json string) string {
	json = trim(json)
	if len(json) >= 2 && (json[0] == '[' || json[0] == '{') {
		json = json[1 : len(json)-1]
	}
	return json
}

// DisableModifiers will disable the modifier syntax
var DisableModifiers = false

var modifiers map[string]func(json, arg string) string

func init() {
	modifiers = map[string]func(json, arg string) string{
		"pretty":  modPretty,
		"ugly":    modUgly,
		"reverse": modReverse,
		"this":    modThis,
		"flatten": modFlatten,
		"join":    modJoin,
		"valid":   modValid,
		"keys":    modKeys,
		"values":  modValues,
		"tostr":   modToStr,
		"fromstr": modFromStr,
		"group":   modGroup,
		"dig":     modDig,
	}
}

// AddModifier binds a custom modifier command to the GJSON syntax.
// This operation is not thread safe and should be executed prior to
// using all other gjson function.
func AddModifier(name string, fn func(json, arg string) string) {
	modifiers[name] = fn
}

// ModifierExists returns true when the specified modifier exists.
func ModifierExists(name string, fn func(json, arg string) string) bool {
	_, ok := modifiers[name]
	return ok
}

// cleanWS remove any non-whitespace from string
func cleanWS(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			var s2 []byte
			for i := 0; i < len(s); i++ {
				switch s[i] {
				case ' ', '\t', '\n', '\r':
					s2 = append(s2, s[i])
				}
			}
			return string(s2)
		}
	}
	return s
}

// @pretty modifier makes the json look nice.
func modPretty(json, arg string) string {
	if len(arg) > 0 {
		opts := *pretty.DefaultOptions
		Parse(arg).ForEach(func(key, value Result) bool {
			switch key.String() {
			case "sortKeys":
				opts.SortKeys = value.Bool()
			case "indent":
				opts.Indent = cleanWS(value.String())
			case "prefix":
				opts.Prefix = cleanWS(value.String())
			case "width":
				opts.Width = int(value.Int())
			}
			return true
		})
		return bytesString(pretty.PrettyOptions(stringBytes(json), &opts))
	}
	return bytesString(pretty.Pretty(stringBytes(json)))
}

// @this returns the current element. Can be used to retrieve the root element.
func modThis(json, arg string) string {
	return json
}

// @ugly modifier removes all whitespace.
func modUgly(json, arg string) string {
	return bytesString(pretty.Ugly(stringBytes(json)))
}

// @reverse reverses array elements or root object members.
func modReverse(json, arg string) string {
	res := Parse(json)
	if res.IsArray() {
		var values []Result
		res.ForEach(func(_, value Result) bool {
			values = append(values, value)
			return true
		})
		out := make([]byte, 0, len(json))
		out = append(out, '[')
		for i, j := len(values)-1, 0; i >= 0; i, j = i-1, j+1 {
			if j > 0 {
				out = append(out, ',')
			}
			out = append(out, values[i].Raw...)
		}
		out = append(out, ']')
		return bytesString(out)
	}
	if res.IsObject() {
		var keyValues []Result
		res.ForEach(func(key, value Result) bool {
			keyValues = append(keyValues, key, value)
			return true
		})
		out := make([]byte, 0, len(json))
		out = append(out, '{')
		for i, j := len(keyValues)-2, 0; i >= 0; i, j = i-2, j+1 {
			if j > 0 {
				out = append(out, ',')
			}
			out = append(out, keyValues[i+0].Raw...)
			out = append(out, ':')
			out = append(out, keyValues[i+1].Raw...)
		}
		out = append(out, '}')
		return bytesString(out)
	}
	return json
}

// @flatten an array with child arrays.
//
//	[1,[2],[3,4],[5,[6,7]]] -> [1,2,3,4,5,[6,7]]
//
// The {"deep":true} arg can be provide for deep flattening.
//
//	[1,[2],[3,4],[5,[6,7]]] -> [1,2,3,4,5,6,7]
//
// The original json is returned when the json is not an array.
func modFlatten(json, arg string) string {
	res := Parse(json)
	if !res.IsArray() {
		return json
	}
	var deep bool
	if arg != "" {
		Parse(arg).ForEach(func(key, value Result) bool {
			if key.String() == "deep" {
				deep = value.Bool()
			}
			return true
		})
	}
	var out []byte
	out = append(out, '[')
	var idx int
	res.ForEach(func(_, value Result) bool {
		var raw string
		if value.IsArray() {
			if deep {
				raw = unwrap(modFlatten(value.Raw, arg))
			} else {
				raw = unwrap(value.Raw)
			}
		} else {
			raw = value.Raw
		}
		raw = strings.TrimSpace(raw)
		if len(raw) > 0 {
			if idx > 0 {
				out = append(out, ',')
			}
			out = append(out, raw...)
			idx++
		}
		return true
	})
	out = append(out, ']')
	return bytesString(out)
}

// @keys extracts the keys from an object.
//
//	{"first":"Tom","last":"Smith"} -> ["first","last"]
func modKeys(json, arg string) string {
	v := Parse(json)
	if !v.Exists() {
		return "[]"
	}
	obj := v.IsObject()
	var out strings.Builder
	out.WriteByte('[')
	var i int
	v.ForEach(func(key, _ Result) bool {
		if i > 0 {
			out.WriteByte(',')
		}
		if obj {
			out.WriteString(key.Raw)
		} else {
			out.WriteString("null")
		}
		i++
		return true
	})
	out.WriteByte(']')
	return out.String()
}

// @values extracts the values from an object.
//
//	{"first":"Tom","last":"Smith"} -> ["Tom","Smith"]
func modValues(json, arg string) string {
	v := Parse(json)
	if !v.Exists() {
		return "[]"
	}
	if v.IsArray() {
		return json
	}
	var out strings.Builder
	out.WriteByte('[')
	var i int
	v.ForEach(func(_, value Result) bool {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(value.Raw)
		i++
		return true
	})
	out.WriteByte(']')
	return out.String()
}

// @join multiple objects into a single object.
//
//	[{"first":"Tom"},{"last":"Smith"}] -> {"first","Tom","last":"Smith"}
//
// The arg can be "true" to specify that duplicate keys should be preserved.
//
//	[{"first":"Tom","age":37},{"age":41}] -> {"first","Tom","age":37,"age":41}
//
// Without preserved keys:
//
//	[{"first":"Tom","age":37},{"age":41}] -> {"first","Tom","age":41}
//
// The original json is returned when the json is not an object.
func modJoin(json, arg string) string {
	res := Parse(json)
	if !res.IsArray() {
		return json
	}
	var preserve bool
	if arg != "" {
		Parse(arg).ForEach(func(key, value Result) bool {
			if key.String() == "preserve" {
				preserve = value.Bool()
			}
			return true
		})
	}
	var out []byte
	out = append(out, '{')
	if preserve {
		// Preserve duplicate keys.
		var idx int
		res.ForEach(func(_, value Result) bool {
			if !value.IsObject() {
				return true
			}
			if idx > 0 {
				out = append(out, ',')
			}
			out = append(out, unwrap(value.Raw)...)
			idx++
			return true
		})
	} else {
		// Deduplicate keys and generate an object with stable ordering.
		var keys []Result
		kvals := make(map[string]Result)
		res.ForEach(func(_, value Result) bool {
			if !value.IsObject() {
				return true
			}
			value.ForEach(func(key, value Result) bool {
				k := key.String()
				if _, ok := kvals[k]; !ok {
					keys = append(keys, key)
				}
				kvals[k] = value
				return true
			})
			return true
		})
		for i := 0; i < len(keys); i++ {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, keys[i].Raw...)
			out = append(out, ':')
			out = append(out, kvals[keys[i].String()].Raw...)
		}
	}
	out = append(out, '}')
	return bytesString(out)
}

// @valid ensures that the json is valid before moving on. An empty string is
// returned when the json is not valid, otherwise it returns the original json.
func modValid(json, arg string) string {
	if !Valid(json) {
		return ""
	}
	return json
}

// @fromstr converts a string to json
//
//	"{\"id\":1023,\"name\":\"alert\"}" -> {"id":1023,"name":"alert"}
func modFromStr(json, arg string) string {
	if !Valid(json) {
		return ""
	}
	return Parse(json).String()
}

// @tostr converts a string to json
//
//	{"id":1023,"name":"alert"} -> "{\"id\":1023,\"name\":\"alert\"}"
func modToStr(str, arg string) string {
	return string(AppendJSONString(nil, str))
}

func modGroup(json, arg string) string {
	res := Parse(json)
	if !res.IsObject() {
		return ""
	}
	var all [][]byte
	res.ForEach(func(key, value Result) bool {
		if !value.IsArray() {
			return true
		}
		var idx int
		value.ForEach(func(_, value Result) bool {
			if idx == len(all) {
				all = append(all, []byte{})
			}
			all[idx] = append(all[idx], ("," + key.Raw + ":" + value.Raw)...)
			idx++
			return true
		})
		return true
	})
	var data []byte
	data = append(data, '[')
	for i, item := range all {
		if i > 0 {
			data = append(data, ',')
		}
		data = append(data, '{')
		data = append(data, item[1:]...)
		data = append(data, '}')
	}
	data = append(data, ']')
	return string(data)
}

// stringHeader instead of reflect.StringHeader
type stringHeader struct {
	data unsafe.Pointer
	len  int
}

// sliceHeader instead of reflect.SliceHeader
type sliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

// getBytes casts the input json bytes to a string and safely returns the
// results as uniquely allocated data. This operation is intended to minimize
// copies and allocations for the large json string->[]byte.
func getBytes(json []byte, path string) Result {
	var result Result
	if json != nil {
		// unsafe cast to string
		result = Get(*(*string)(unsafe.Pointer(&json)), path)
		// safely get the string headers
		rawhi := *(*stringHeader)(unsafe.Pointer(&result.Raw))
		strhi := *(*stringHeader)(unsafe.Pointer(&result.Str))
		// create byte slice headers
		rawh := sliceHeader{data: rawhi.data, len: rawhi.len, cap: rawhi.len}
		strh := sliceHeader{data: strhi.data, len: strhi.len, cap: rawhi.len}
		if strh.data == nil {
			// str is nil
			if rawh.data == nil {
				// raw is nil
				result.Raw = ""
			} else {
				// raw has data, safely copy the slice header to a string
				result.Raw = string(*(*[]byte)(unsafe.Pointer(&rawh)))
			}
			result.Str = ""
		} else if rawh.data == nil {
			// raw is nil
			result.Raw = ""
			// str has data, safely copy the slice header to a string
			result.Str = string(*(*[]byte)(unsafe.Pointer(&strh)))
		} else if uintptr(strh.data) >= uintptr(rawh.data) &&
			uintptr(strh.data)+uintptr(strh.len) <=
				uintptr(rawh.data)+uintptr(rawh.len) {
			// Str is a substring of Raw.
			start := uintptr(strh.data) - uintptr(rawh.data)
			// safely copy the raw slice header
			result.Raw = string(*(*[]byte)(unsafe.Pointer(&rawh)))
			// substring the raw
			result.Str = result.Raw[start : start+uintptr(strh.len)]
		} else {
			// safely copy both the raw and str slice headers to strings
			result.Raw = string(*(*[]byte)(unsafe.Pointer(&rawh)))
			result.Str = string(*(*[]byte)(unsafe.Pointer(&strh)))
		}
	}
	return result
}

// fillIndex finds the position of Raw data and assigns it to the Index field
// of the resulting value. If the position cannot be found then Index zero is
// used instead.
func fillIndex(json string, c *parseContext) {
	if len(c.value.Raw) > 0 && !c.calcd {
		jhdr := *(*stringHeader)(unsafe.Pointer(&json))
		rhdr := *(*stringHeader)(unsafe.Pointer(&(c.value.Raw)))
		c.value.Index = int(uintptr(rhdr.data) - uintptr(jhdr.data))
		if c.value.Index < 0 || c.value.Index >= len(json) {
			c.value.Index = 0
		}
	}
}

func stringBytes(s string) []byte {
	return *(*[]byte)(unsafe.Pointer(&sliceHeader{
		data: (*stringHeader)(unsafe.Pointer(&s)).data,
		len:  len(s),
		cap:  len(s),
	}))
}

func bytesString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

func revSquash(json string) string {
	// reverse squash
	// expects that the tail character is a ']' or '}' or ')' or '"'
	// squash the value, ignoring all nested arrays and objects.
	i := len(json) - 1
	var depth int
	if json[i] != '"' {
		depth++
	}
	if json[i] == '}' || json[i] == ']' || json[i] == ')' {
		i--
	}
	for ; i >= 0; i-- {
		switch json[i] {
		case '"':
			i--
			for ; i >= 0; i-- {
				if json[i] == '"' {
					esc := 0
					for i > 0 && json[i-1] == '\\' {
						i--
						esc++
					}
					if esc%2 == 1 {
						continue
					}
					i += esc
					break
				}
			}
			if depth == 0 {
				if i < 0 {
					i = 0
				}
				return json[i:]
			}
		case '}', ']', ')':
			depth++
		case '{', '[', '(':
			depth--
			if depth == 0 {
				return json[i:]
			}
		}
	}
	return json
}

// Paths returns the original GJSON paths for a Result where the Result came
// from a simple query path that returns an array, like:
//
//	gjson.Get(json, "friends.#.first")
//
// The returned value will be in the form of a JSON array:
//
//	["friends.0.first","friends.1.first","friends.2.first"]
//
// The param 'json' must be the original JSON used when calling Get.
//
// Returns an empty string if the paths cannot be determined, which can happen
// when the Result came from a path that contained a multipath, modifier,
// or a nested query.
func (t Result) Paths(json string) []string {
	if t.Indexes == nil {
		return nil
	}
	paths := make([]string, 0, len(t.Indexes))
	t.ForEach(func(_, value Result) bool {
		paths = append(paths, value.Path(json))
		return true
	})
	if len(paths) != len(t.Indexes) {
		return nil
	}
	return paths
}

// Path returns the original GJSON path for a Result where the Result came
// from a simple path that returns a single value, like:
//
//	gjson.Get(json, "friends.#(last=Murphy)")
//
// The returned value will be in the form of a JSON string:
//
//	"friends.0"
//
// The param 'json' must be the original JSON used when calling Get.
//
// Returns an empty string if the paths cannot be determined, which can happen
// when the Result came from a path that contained a multipath, modifier,
// or a nested query.
func (t Result) Path(json string) string {
	var path []byte
	var comps []string // raw components
	i := t.Index - 1
	if t.Index+len(t.Raw) > len(json) {
		// JSON cannot safely contain Result.
		goto fail
	}
	if !strings.HasPrefix(json[t.Index:], t.Raw) {
		// Result is not at the JSON index as expected.
		goto fail
	}
	for ; i >= 0; i-- {
		if json[i] <= ' ' {
			continue
		}
		if json[i] == ':' {
			// inside of object, get the key
			for ; i >= 0; i-- {
				if json[i] != '"' {
					continue
				}
				break
			}
			raw := revSquash(json[:i+1])
			i = i - len(raw)
			comps = append(comps, raw)
			// key gotten, now squash the rest
			raw = revSquash(json[:i+1])
			i = i - len(raw)
			i++ // increment the index for next loop step
		} else if json[i] == '{' {
			// Encountered an open object. The original result was probably an
			// object key.
			goto fail
		} else if json[i] == ',' || json[i] == '[' {
			// inside of an array, count the position
			var arrIdx int
			if json[i] == ',' {
				arrIdx++
				i--
			}
			for ; i >= 0; i-- {
				if json[i] == ':' {
					// Encountered an unexpected colon. The original result was
					// probably an object key.
					goto fail
				} else if json[i] == ',' {
					arrIdx++
				} else if json[i] == '[' {
					comps = append(comps, strconv.Itoa(arrIdx))
					break
				} else if json[i] == ']' || json[i] == '}' || json[i] == '"' {
					raw := revSquash(json[:i+1])
					i = i - len(raw) + 1
				}
			}
		}
	}
	if len(comps) == 0 {
		if DisableModifiers {
			goto fail
		}
		return "@this"
	}
	for i := len(comps) - 1; i >= 0; i-- {
		rcomp := Parse(comps[i])
		if !rcomp.Exists() {
			goto fail
		}
		comp := Escape(rcomp.String())
		path = append(path, '.')
		path = append(path, comp...)
	}
	if len(path) > 0 {
		path = path[1:]
	}
	return string(path)
fail:
	return ""
}

// isSafePathKeyChar returns true if the input character is safe for not
// needing escaping.
func isSafePathKeyChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c <= ' ' || c > '~' || c == '_' ||
		c == '-' || c == ':'
}

// Escape returns an escaped path component.
//
//	json := `{
//	  "user":{
//	     "first.name": "Janet",
//	     "last.name": "Prichard"
//	   }
//	}`
//	user := gjson.Get(json, "user")
//	println(user.Get(gjson.Escape("first.name"))
//	println(user.Get(gjson.Escape("last.name"))
//	// Output:
//	// Janet
//	// Prichard
func Escape(comp string) string {
	for i := 0; i < len(comp); i++ {
		if !isSafePathKeyChar(comp[i]) {
			ncomp := make([]byte, len(comp)+1)
			copy(ncomp, comp[:i])
			ncomp = ncomp[:i]
			for ; i < len(comp); i++ {
				if !isSafePathKeyChar(comp[i]) {
					ncomp = append(ncomp, '\\')
				}
				ncomp = append(ncomp, comp[i])
			}
			return string(ncomp)
		}
	}
	return comp
}

func parseRecursiveDescent(all []Result, parent Result, path string) []Result {
	if res := parent.Get(path); res.Exists() {
		all = append(all, res)
	}
	if parent.IsArray() || parent.IsObject() {
		parent.ForEach(func(_, val Result) bool {
			all = parseRecursiveDescent(all, val, path)
			return true
		})
	}
	return all
}

func modDig(json, arg string) string {
	all := parseRecursiveDescent(nil, Parse(json), arg)
	var out []byte
	out = append(out, '[')
	for i, res := range all {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, res.Raw...)
	}
	out = append(out, ']')
	return string(out)
}
