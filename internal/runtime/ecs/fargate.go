package ecs

import (
	"fmt"
	"strconv"
)

// fargateResources picks the smallest Fargate CPU and memory that covers the
// request. cpu is CPU units ("256") and memory is MiB ("512").
//
// The matrix is the Linux Fargate size table: 0.25 vCPU is 256 units and
// allows 512, 1024, or 2048 MiB; larger sizes step by 1024 MiB, and 8 vCPU
// steps by 4096 MiB from 16384.
func fargateResources(cpuMilli int, memoryBytes int64) (string, string, error) {
	cpu := cpuUnits(cpuMilli)
	if cpu == 0 {
		return "", "", fmt.Errorf("fargate has no cpu size for %d milli-cpu", cpuMilli)
	}
	need := int((memoryBytes + (1 << 20) - 1) >> 20)
	if need < 1 {
		need = 1
	}
	mem, ok := smallestMemory(cpu, need)
	if !ok {
		return "", "", fmt.Errorf("fargate has no size that covers %d cpu units and %d MiB", cpu, need)
	}
	return strconv.Itoa(cpu), strconv.Itoa(mem), nil
}

func cpuUnits(milli int) int {
	switch {
	case milli <= 0:
		return 0
	case milli <= 250:
		return 256
	case milli <= 500:
		return 512
	case milli <= 1000:
		return 1024
	case milli <= 2000:
		return 2048
	case milli <= 4000:
		return 4096
	case milli <= 8000:
		return 8192
	default:
		return 0
	}
}

func smallestMemory(cpu, need int) (int, bool) {
	if cpu == 256 {
		for _, mem := range []int{512, 1024, 2048} {
			if mem >= need {
				return mem, true
			}
		}
		return 0, false
	}
	min, max, step := memoryBounds(cpu)
	if min == 0 {
		return 0, false
	}
	if need < min {
		need = min
	}
	if rem := need % step; rem != 0 {
		need += step - rem
	}
	if need > max {
		return 0, false
	}
	return need, true
}

func memoryBounds(cpu int) (min, max, step int) {
	switch cpu {
	case 512:
		return 1024, 4096, 1024
	case 1024:
		return 2048, 8192, 1024
	case 2048:
		return 4096, 16384, 1024
	case 4096:
		return 8192, 30720, 1024
	case 8192:
		return 16384, 61440, 4096
	default:
		return 0, 0, 1
	}
}
