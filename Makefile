.PHONY: build test clean

build: test
	go build -o kiln .

test:
	go test -race ./...

clean:
	rm -f kiln
