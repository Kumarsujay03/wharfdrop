.PHONY: build test vet fmt release clean

build:
	go build -o bin/wharfdrop ./cmd/wharfdrop

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

release:
	./build.sh

clean:
	rm -rf bin dist
