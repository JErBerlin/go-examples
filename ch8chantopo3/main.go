// Topology 3 is an implementation with channels of a concurrency pattern
// which solves the problem:
// To process a fixed number of tasks while preserving the original order.
// It uses a Pipeline Topology combined with a Fan-Out/Fan-In Worker Pool
// with Channels of Channels (chan chan Result).

package main

import "fmt"

// nTasks is the fixed number of tasks.
const nTasks = 20

// maxWorkers is a chosen number of workers.
const maxWorkers = 8

type data int
type Result struct{ Data data }

// A taskEnvelope will be scheduled to be sent to the workers.
type taskEnvelope struct {
	task  data        // The actual work to do
	reply chan Result // The private, unbuffered channel to reply with result
}

func main() {
	allTasks := make([]data, nTasks)
	for i := range nTasks {
		allTasks[i] = data(i)
	} // example data is arithmetic succession

	tasksCh := make(chan taskEnvelope, nTasks)
	orderCh := make(chan chan Result, nTasks)

	// Fan-out producer
	for _, task := range allTasks {
		reply := make(chan Result)

		// 1. Send to workers for concurrent exectuion
		tasksCh <- taskEnvelope{task: task, reply: reply}

		// 2. Send the reply channel to ordering channel to track the order
		orderCh <- reply
	}
	close(tasksCh) // No more tasks will be created
	close(orderCh) // No more order placeholders will be tracked

	// A fixed number of workers pull envelopes from tasksCh
	for _ = range maxWorkers {
		go func() {
			for envelope := range tasksCh {
				processed := process(envelope.task)
				// The worker will hold up result until earlier ordered tasks are sequenced
				envelope.reply <- Result{processed} // Blocks until Sequencer is ready
			}
		}()
	}

	// Sequencer / Fan-in consumer
	for reply := range orderCh {
		res := <-reply // Blocks each channel in the exact order they were generated
		handleResultOrdered(res)
	}

}

// process is an example processing function of task data, wich returns data
func process(d data) data {
	return d
}

// handleResultOrdered is an example result gathering / post-processing function
func handleResultOrdered(r Result) {
	fmt.Println(r.Data)
}
