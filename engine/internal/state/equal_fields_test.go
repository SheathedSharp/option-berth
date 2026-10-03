package state

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Walk the actual wire structs rather than a hand-written field list. A new
// scalar or nested field automatically becomes a required comparison case.
func TestTypedEqualityCoversWireFields(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		equal   func(any, any) bool
		ignored func(string) bool
	}{
		{"port", Port{}, func(a, b any) bool { x, y := a.(Port), b.(Port); return portsEqual(&x, &y, true) }, func(s string) bool { return s == "Health.LatencyMs" || s == "Health.ObservedAt" }},
		{"service", Service{}, func(a, b any) bool { x, y := a.(Service), b.(Service); return serviceEqual(&x, &y) }, func(s string) bool { return s == "HealthStatus.LatencyMs" || s == "HealthStatus.ObservedAt" }},
		{"group", Group{}, func(a, b any) bool { return groupsEqual(a.(Group), b.(Group)) }, func(s string) bool {
			return s == "Host" || s == "Name" || strings.HasSuffix(s, "HealthStatus.LatencyMs") || strings.HasSuffix(s, "HealthStatus.ObservedAt")
		}},
	}
	for _, tc := range cases {
		for _, path := range scalarPaths(reflect.TypeOf(tc.value), nil) {
			t.Run(tc.name+"/"+strings.Join(path, "."), func(t *testing.T) {
				a, b := reflect.New(reflect.TypeOf(tc.value)).Elem(), reflect.New(reflect.TypeOf(tc.value)).Elem()
				x, y := scalarAt(a, path), scalarAt(b, path)
				// Both start with the same allocated shape; change just this leaf.
				_ = x
				switch y.Kind() {
				case reflect.String:
					y.SetString("changed")
				case reflect.Bool:
					y.SetBool(true)
				case reflect.Int, reflect.Int64:
					y.SetInt(7)
				case reflect.Float64:
					y.SetFloat(1.25)
				default:
					t.Fatalf("add mutation for %s", y.Kind())
				}
				want := tc.ignored(strings.Join(path, "."))
				if got := tc.equal(a.Interface(), b.Interface()); got != want {
					t.Fatalf("equality=%v want=%v: unaccounted wire field", got, want)
				}
			})
		}
	}
}

func scalarPaths(typ reflect.Type, path []string) [][]string {
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice:
		return scalarPaths(typ.Elem(), path)
	case reflect.Struct:
		var out [][]string
		for i := 0; i < typ.NumField(); i++ {
			out = append(out, scalarPaths(typ.Field(i).Type, append(append([]string(nil), path...), typ.Field(i).Name))...)
		}
		return out
	default:
		return [][]string{path}
	}
}

func scalarAt(v reflect.Value, path []string) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		return scalarAt(v.Elem(), path)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		return scalarAt(v.Index(0), path)
	case reflect.Struct:
		return scalarAt(v.FieldByName(path[0]), path[1:])
	default:
		return v
	}
}

func TestTypedEqualityPreservesNilAndNaN(t *testing.T) {
	empty := ""
	cases := []struct{ a, b Port }{
		{Port{}, Port{BindAddresses: []string{}}},
		{Port{}, Port{Name: &empty}},
		{Port{Stats: &Stats{CPUPercent: math.NaN()}}, Port{Stats: &Stats{CPUPercent: math.NaN()}}},
		{Port{Stats: &Stats{CPUPercent: math.Inf(1)}}, Port{Stats: &Stats{CPUPercent: math.Inf(1)}}},
	}
	shared := &Stats{CPUPercent: math.NaN()}
	cases = append(cases, struct{ a, b Port }{Port{Stats: shared}, Port{Stats: shared}})
	for i, tc := range cases {
		for _, stats := range []bool{false, true} {
			if got, want := portsEqual(&tc.a, &tc.b, stats), oraclePortsEqual(tc.a, tc.b, stats); got != want {
				t.Fatalf("case %d stats %v: %v != %v", i, stats, got, want)
			}
		}
	}
	groups := [][2]Group{
		{{}, {Services: []Service{}}},
		{{}, {Members: []int{}}},
		{{}, {Machine: []MachineRef{}}},
		{{Services: []Service{{Name: "api"}}}, {Services: []Service{{Name: "api", DependsOn: []string{}}}}},
		{{Machine: []MachineRef{{}}}, {Machine: []MachineRef{{Unit: &empty}}}},
	}
	for i, tc := range groups {
		got := Diff(Snapshot{Groups: []Group{tc[0]}}, Snapshot{Groups: []Group{tc[1]}})
		want := oracleDiff(Snapshot{Groups: []Group{tc[0]}}, Snapshot{Groups: []Group{tc[1]}})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("group %d: %+v != %+v", i, got, want)
		}
	}
}
