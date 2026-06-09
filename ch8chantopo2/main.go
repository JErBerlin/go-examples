// Topology 2 is an implementation with channels of a concurrency pattern
// which solves the problem:
// To process a variable number of tasks (stream) whithout preserving the original order.

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
	// Set up the throttling channels
	// Use reasonable buffer, the same for tasks and order channel
	const buffer = 5
	tasksCh := make(chan data, buffer)
	resultsCh := make(chan Result, buffer)

	// Initialize the input tasks stream
	stream := &TaskStream{} // Starts at 0

	// Fan-out producer / task generator as background stream reader
	go func() {
		// Keep reading until the stream source is exhausted
		for stream.Next() {
			task := stream.GetTask()
			tasksCh <- task // blocks if workers are busy (backpressure)
		}
		close(tasksCh) // stream ended: signal to workers no more work coming
	}()

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

// TaskStream is an example streamer of tasks
type TaskStream struct {
	val int
}

func (s *TaskStream) Next() bool {
	// Stops the stream at 20 items.
	if s.val < 20 {
		return true
	}
	return false
}

func (s *TaskStream) GetTask() data {
	task := data(s.val)
	s.val++ // Advance the count for the next iteration
	return task
}
