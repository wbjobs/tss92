package main

import (
	"time"
)

func profileTarget() interface{} {
	data := make([]int, 0, 10000)
	for i := 0; i < 10000; i++ {
		data = append(data, i*i)
	}
	total := 0
	for _, v := range data {
		total += v
	}
	time.Sleep(100 * time.Millisecond)
	return total
}
