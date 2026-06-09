// Crawl1 is a concurrent implementation of a web crawler

/* This is an example taken from the Go Book by Kernighan and Donovan.
The main function follows a breadthFirst (§5.6). A worklist records the queue of
items that need processing, each item being a list of URLs to crawl, but instead
of representing the queue using a slice as usual, we use achannel.
Each call to crawl occurs in its own goroutine and sends the links it discovers
back to the worklist.
*/

package main

import (
	"fmt"
	"log/slog"
	"os"

	"gopl.io/ch5/links"
)

func main() {
	worklist := make(chan []string)
	var n int // number of pending sends to worklist

	// Start with the command line arguments.
	// We assume the command call has proper formatted args: one url with schema.i
	n++
	go func() { worklist <- os.Args[1:] }()

	// Crawl the web concurrently.
	seen := make(map[string]bool)
	for ; n > 0; n-- {
		list := <-worklist
		for _, link := range list {
			if !seen[link] {
				seen[link] = true
				n++
				go func(link string) {
					worklist <- crawl(link)
				}(link)
			}
		}
	}
}

// tokens is a counting semaphore used to enforce a limit of cuncurrent requests.
var tokens = make(chan struct{}, 2)

func crawl(url string) []string {
	fmt.Println(url)
	tokens <- struct{}{} // acquire token
	list, err := links.Extract(url)
	<-tokens // release token
	if err != nil {
		slog.Error(err.Error())
	}
	return list
}
