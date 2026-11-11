package ecs

import "testing"

func TestFargateResources(t *testing.T) {
	t.Parallel()
	cases := []struct {
		milli int
		mem   int64
		cpu   string
		want  string
	}{
		{milli: 100, mem: 64 << 20, cpu: "256", want: "512"},
		{milli: 250, mem: 512 << 20, cpu: "256", want: "512"},
		{milli: 500, mem: 256 << 20, cpu: "512", want: "1024"},
		{milli: 500, mem: 1024 << 20, cpu: "512", want: "1024"},
		{milli: 1000, mem: 2048 << 20, cpu: "1024", want: "2048"},
		{milli: 2000, mem: 5000 << 20, cpu: "2048", want: "5120"},
		{milli: 4000, mem: 8 << 30, cpu: "4096", want: "8192"},
		{milli: 8000, mem: 8 << 30, cpu: "8192", want: "16384"},
	}
	for _, tc := range cases {
		cpu, mem, err := fargateResources(tc.milli, tc.mem)
		if err != nil || cpu != tc.cpu || mem != tc.want {
			t.Fatalf("milli %d mem %d -> %s %s %v", tc.milli, tc.mem, cpu, mem, err)
		}
	}
	if _, _, err := fargateResources(9000, 512<<20); err == nil {
		t.Fatal("expected no Fargate size above 8 vCPU")
	}
}
