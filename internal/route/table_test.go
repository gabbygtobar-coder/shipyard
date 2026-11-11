package route

import "testing"

func TestTableSwapAndClear(t *testing.T) {
	t.Parallel()
	table := NewTable()
	if _, ok := table.Lookup("demo"); ok {
		t.Fatal("empty table returned a target")
	}
	first := Target{Endpoint: "http://127.0.0.1:1", InstanceID: "a", DeploymentID: "dep_1"}
	if prev := table.Swap("demo", first); prev.InstanceID != "" {
		t.Fatalf("first swap previous = %#v", prev)
	}
	got, ok := table.Lookup("demo")
	if !ok || got != first {
		t.Fatalf("lookup = %#v %v", got, ok)
	}
	second := Target{Endpoint: "http://127.0.0.1:2", InstanceID: "b", DeploymentID: "dep_2"}
	prev := table.Swap("demo", second)
	if prev != first {
		t.Fatalf("previous = %#v", prev)
	}
	cleared, ok := table.Clear("demo")
	if !ok || cleared != second {
		t.Fatalf("clear = %#v %v", cleared, ok)
	}
	if _, ok := table.Lookup("demo"); ok {
		t.Fatal("route remained after clear")
	}
	if _, ok := table.Clear("demo"); ok {
		t.Fatal("second clear reported a route")
	}
}
