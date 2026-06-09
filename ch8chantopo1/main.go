package main

import (
	"fmt"
	"sync"
	"time"
)

// nTasks is the fixed number of tasks.
const nTasks = 20

// maxWorkers is a chosen number of workers.
const maxWorkers = 8

type data int

type Result struct{ Data data }

func main() {
	allTasks := make([]data, nTasks)
	for i := range nTasks {
		allTasks[i] = data(i)
	} // example data is arithmetic succession

	tasksCh := make(chan data, nTasks)
	resultsCh := make(chan Result, nTasks)

	// Fan-out producer
	// Populate and close tasks channel immediately
	for _, task := range allTasks {
		tasksCh <- task
	}
	close(tasksCh) // signals to workers that no more work is coming

	// Spin up worker pool
	var wg sync.WaitGroup // tracks active workers
	for _ = range maxWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range tasksCh {
				resultsCh <- Result{Data: process(d)} // never blocks because enough capacity
			}
		}()
	}

	// Workers' lifecycle coordinator / supervisor
	// In general it has to run as background process in a goroutine, not sequentially.
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Start processing results on-the-fly, without waiting for all tasks to be done.
	for res := range resultsCh {
		handleResult(res)
	}
}

// process is an example processing function of task data, wich returns data
func process(task data) data {
	// Task 0 takes longer to prove that order does NOT matter.
	// Younger tasks (like 1 and 2) will finish and print first.
	if task == 0 {
		time.Sleep(50 * time.Millisecond)
	} else {
		time.Sleep(5 * time.Millisecond)
	}
	return task * 10
}

// handleResult is an example result gathering / post-processing function
func handleResult(r Result) {
	fmt.Println(r.Data)
}
