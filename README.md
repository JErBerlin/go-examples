# Go-examples

This is a collection of examples of basic go programs, each focused in one topic or need and one specific way of implement it. Most examples come from the book of Donovan and Kernighan `The Go Programming Language`.


## How to Run the Examples

- After fetching the repo, go to the corresponding directory.
- From there just:
```go
    go run .
```

## To add new modules (programs)

The example collection is organised as a go work space with several modules (one per runnable program main).

To add a new module:
- Create first a new directory, for instance `/newexample`. 
- Initialise a new go module:
```go
    go mod init newexample

```
- Then from the root directory:
```go
    go work use ./newexample

```

This will add the new module to the list in the `go.work` file.
