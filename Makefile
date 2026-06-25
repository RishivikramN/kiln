.PHONY: build test clean

build: test
	go build -o kiln .

test:
	go test -race -count=1 ./...

clean:
	rm -f kiln
